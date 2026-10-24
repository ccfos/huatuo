// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
 * Record BPF object identities before the tracee can close their FDs. Seccomp
 * selects resource operations; ordinary syscalls run without ptrace stops.
 * All traced tasks are stopped during each selected syscall, because another
 * thread sharing the FD table could otherwise close/reuse its returned FD.
 * This tool supplies observations only. Shell owns the reclamation assertions.
 */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <linux/audit.h>
#include <linux/bpf.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <sched.h>
#include <signal.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/ptrace.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/uio.h>
#include <sys/user.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>
#ifdef __aarch64__
#include <asm/ptrace.h>
#include <elf.h>
#endif

#if defined(__x86_64__)
#define OBSERVER_ARCH AUDIT_ARCH_X86_64
#elif defined(__aarch64__)
#define OBSERVER_ARCH AUDIT_ARCH_AARCH64
#else
#error "BPF observer supports native x86-64 and AArch64"
#endif

#define MAX_TASKS 4096

/* These stable UAPI commands are absent from pre-5.8 distro headers. */
enum {
	OBSERVER_LINK_CREATE = 28,
	OBSERVER_LINK_UPDATE = 29,
	OBSERVER_LINK_GET_FD_BY_ID = 30,
	OBSERVER_SYSCALL_PROGRAM = 31,
	OBSERVER_CLONE3_SYSCALL = 435,
};

enum observation_event {
	RESOURCE_OPERATION,
	FOREIGN_SYSCALL_ABI,
	UNSUPPORTED_MUTATION,
	INSPECT_CLONE_FLAGS,
	INSPECT_SECCOMP_COMMAND,
};

struct traced_task {
	pid_t pid;
	pid_t vfork_parent;
	bool stopped;
	bool operation;
	bool exiting;
	int signal;
};

struct object_id {
	const char *kind;
	unsigned int id;
	unsigned int program_type;
};

struct operation {
	unsigned int command;
	unsigned long attributes;
	unsigned long argument0;
	struct object_id pinned;
	struct object_id attachment_program;
	char pin_path[PATH_MAX];
	bool pin_valid;
};

static struct traced_task tasks[MAX_TASKS];
static sigset_t observer_signals;
static const char *output_directory;
static FILE *inventory;
static pid_t root_pid;
static int root_status;
static bool complete;
static bool root_alive = true;
static bool term_forwarded;
static unsigned long record_count;

static void write_completion(void)
{
	char temporary[PATH_MAX];
	char destination[PATH_MAX];
	FILE *file;

	if (!output_directory)
		return;
	if (snprintf(temporary, sizeof(temporary), "%s/complete.tmp",
		     output_directory) >= (int)sizeof(temporary) ||
	    snprintf(destination, sizeof(destination), "%s/complete.json",
		     output_directory) >= (int)sizeof(destination))
		return;
	file = fopen(temporary, "we");
	if (!file)
		return;
	if (fprintf(file,
		    "{\"complete\":%s,\"root_pid\":%d,\"exit_status\":%d,"
		    "\"sigterm_forwarded\":%s,\"record_count\":%lu}\n",
		    complete ? "true" : "false", root_pid, root_status,
		    term_forwarded ? "true" : "false", record_count) < 0) {
		fclose(file);
		return;
	}
	if (fclose(file) == 0)
		rename(temporary, destination);
}

static void fail(const char *operation, pid_t pid)
{
	fprintf(stderr, "BPF observation failed: %s; tid=%d: %s\n", operation,
		pid, strerror(errno));
	if (root_pid > 0 && root_alive)
		kill(root_pid, SIGKILL);
	exit(1);
}

static void handle_signal(int signal)
{
	if (signal == SIGCHLD)
		return;
	if (signal != SIGTERM) {
		errno = EINTR;
		fail("observer interrupted", root_pid);
	}
	/* The parent never signals an identity it has already reaped. */
	if (!root_alive) {
		errno = ESRCH;
		fail("Agent exited before SIGTERM", root_pid);
	}
	if (kill(root_pid, SIGTERM) < 0)
		fail("forward SIGTERM to Agent", root_pid);
	term_forwarded = true;
}

static struct traced_task *task_for(pid_t pid)
{
	struct traced_task *empty = NULL;
	int i;

	for (i = 0; i < MAX_TASKS; i++) {
		if (tasks[i].pid == pid)
			return &tasks[i];
		if (!tasks[i].pid && !empty)
			empty = &tasks[i];
	}
	if (!empty) {
		errno = EOVERFLOW;
		fail("tracked task limit exceeded", pid);
	}
	empty->pid = pid;
	return empty;
}

static bool tasks_remaining(void)
{
	int i;

	for (i = 0; i < MAX_TASKS; i++)
		if (tasks[i].pid)
			return true;
	return false;
}

static void resume_task(struct traced_task *task, bool syscall_exit)
{
	if (ptrace(syscall_exit ? PTRACE_SYSCALL : PTRACE_CONT, task->pid, NULL,
		   (void *)(long)task->signal) < 0) {
		/* A SIGKILL death may already be queued for waitpid. */
		if (errno != ESRCH)
			fail("resume tracee", task->pid);
	}
	task->stopped = false;
	task->signal = 0;
}

static void read_registers(pid_t pid, struct operation *operation, long *result)
{
#ifdef __x86_64__
	struct user_regs_struct registers;

	if (ptrace(PTRACE_GETREGS, pid, NULL, &registers) < 0)
		fail("read syscall registers", pid);
	operation->command = registers.rdi;
	operation->argument0 = registers.rdi;
	operation->attributes = registers.rsi;
	*result = (long)registers.rax;
#else
	struct user_pt_regs registers;
	struct iovec buffer = {&registers, sizeof(registers)};

	if (ptrace(PTRACE_GETREGSET, pid, (void *)NT_PRSTATUS, &buffer) < 0)
		fail("read syscall registers", pid);
	operation->command = registers.regs[0];
	operation->argument0 = registers.regs[0];
	operation->attributes = registers.regs[1];
	*result = (long)registers.regs[0];
#endif
}

static bool
read_memory(pid_t pid, unsigned long address, void *data, size_t size)
{
	struct iovec remote = {(void *)address, size};
	struct iovec local = {data, size};

	return process_vm_readv(pid, &local, 1, &remote, 1, 0) == (ssize_t)size;
}

static bool resource_event(pid_t pid, unsigned long event)
{
	struct operation operation = {0};
	uint64_t flags;
	long result;

	if (event == RESOURCE_OPERATION)
		return true;
	read_registers(pid, &operation, &result);
	if (event == INSPECT_CLONE_FLAGS &&
	    read_memory(pid, operation.argument0, &flags, sizeof(flags)) &&
	    !(flags & CLONE_UNTRACED))
		return false;
	if (event == INSPECT_SECCOMP_COMMAND && operation.command >= 2)
		return false;
	fprintf(stderr, "unsupported observation event %lu, argument0=%lu\n",
		event, operation.argument0);
	errno = ENOTSUP;
	fail("syscall would bypass BPF observation", pid);
	return false;
}

static struct traced_task *wait_event(bool *syscall_exit)
{
	struct traced_task *task;
	unsigned long message;
	unsigned int event;
	pid_t pid;
	int status;

	*syscall_exit = false;
	for (;;) {
		const struct timespec no_wait = {0};
		int signal;

		while ((signal = sigtimedwait(&observer_signals, NULL,
					      &no_wait)) > 0)
			handle_signal(signal);
		if (errno != EAGAIN)
			fail("read pending observer signals", root_pid);
		pid = waitpid(-1, &status, __WALL | WNOHANG);
		if (pid != 0)
			break;
		/* Signals stay blocked between waitpid and sigwaitinfo: an exit
		 * or shutdown request cannot be lost in that interval. */
		signal = sigwaitinfo(&observer_signals, NULL);
		if (signal < 0)
			fail("wait for tracee or shutdown signal", root_pid);
		handle_signal(signal);
	}
	if (pid < 0)
		fail("wait for tracee", root_pid);
	task = task_for(pid);
	if (WIFEXITED(status) || WIFSIGNALED(status)) {
		if (pid == root_pid) {
			root_alive = false;
			root_status = WIFEXITED(status)
					      ? WEXITSTATUS(status)
					      : 128 + WTERMSIG(status);
		}
		memset(task, 0, sizeof(*task));
		return NULL;
	}
	if (!WIFSTOPPED(status)) {
		errno = EPROTO;
		fail("unexpected wait status", pid);
	}
	task->stopped = true;
	task->signal = 0;
	event = (unsigned int)status >> 16;
	switch (event) {
	case PTRACE_EVENT_FORK:
	case PTRACE_EVENT_VFORK:
	case PTRACE_EVENT_CLONE:
		if (ptrace(PTRACE_GETEVENTMSG, pid, NULL, &message) < 0)
			fail("read descendant identity", pid);
		task_for((pid_t)message)->vfork_parent =
			event == PTRACE_EVENT_VFORK ? pid : 0;
		break;
	case PTRACE_EVENT_EXEC:
		task->vfork_parent = 0;
		if (ptrace(PTRACE_GETEVENTMSG, pid, NULL, &message) < 0)
			fail("read exec identity", pid);
		if ((pid_t)message != pid) {
			struct traced_task *old = task_for((pid_t)message);

			memset(old, 0, sizeof(*old));
		}
		break;
	case PTRACE_EVENT_SECCOMP:
		if (ptrace(PTRACE_GETEVENTMSG, pid, NULL, &message) < 0)
			fail("read seccomp event", pid);
		task->operation = resource_event(pid, message);
		break;
	case PTRACE_EVENT_STOP:
		if (WSTOPSIG(status) != SIGTRAP) {
			errno = ENOTSUP;
			fail("unexpected process group-stop", pid);
		}
		break;
	case PTRACE_EVENT_EXIT:
		task->exiting = true;
		break;
	case 0:
		if (WSTOPSIG(status) == (SIGTRAP | 0x80))
			*syscall_exit = true;
		else
			task->signal = WSTOPSIG(status);
		break;
	default:
		errno = EPROTO;
		fail("unknown ptrace event", pid);
	}
	return task;
}

static void stop_other_tasks(void)
{
	struct traced_task *task;
	bool syscall_exit;
	bool running;
	int i;

	for (i = 0; i < MAX_TASKS; i++) {
		if (!tasks[i].pid || tasks[i].stopped)
			continue;
		if (ptrace(PTRACE_INTERRUPT, tasks[i].pid, NULL, NULL) < 0 &&
		    errno != ESRCH)
			fail("stop task sharing a possible FD table",
			     tasks[i].pid);
	}
	for (;;) {
		running = false;
		for (i = 0; i < MAX_TASKS; i++)
			if (tasks[i].pid && !tasks[i].stopped)
				running = true;
		if (!running)
			return;
		task = wait_event(&syscall_exit);

		/* A vfork parent cannot stop until its child has execed/exited.
		 */
		if (task && task->vfork_parent) {
			if (task->operation) {
				errno = ENOTSUP;
				fail("BPF operation before vfork child exec",
				     task->pid);
			}
			resume_task(task, false);
		}
		if (syscall_exit) {
			errno = EPROTO;
			fail("unmatched syscall exit while stopping tasks",
			     root_pid);
		}
	}
}

static bool read_object_id(pid_t pid, int fd, struct object_id *object)
{
	char path[64];
	char line[256];
	FILE *file;

	snprintf(path, sizeof(path), "/proc/%d/fdinfo/%d", pid, fd);
	file = fopen(path, "re");
	if (!file)
		return false;
	while (fgets(line, sizeof(line), file)) {
		unsigned int id;

		if (sscanf(line, "prog_type: %u", &id) == 1) {
			object->program_type = id;
		} else if (sscanf(line, "link_id: %u", &id) == 1) {
			object->kind = "link";
			object->id = id;
		} else if (sscanf(line, "map_id: %u", &id) == 1) {
			object->kind = "map";
			object->id = id;
		} else if (!object->kind &&
			   sscanf(line, "prog_id: %u", &id) == 1) {
			object->kind = "prog";
			object->id = id;
		}
	}
	if (ferror(file)) {
		int error = errno ? errno : EIO;

		fclose(file);
		errno = error;
		return false;
	}
	if (fclose(file))
		return false;
	if (object->kind && object->id)
		return true;
	errno = ENODATA;
	return false;
}

static void capture_pin(pid_t pid, struct operation *operation)
{
	struct {
		uint64_t pathname;
		uint32_t fd;
		uint32_t flags;
	} attributes;
	size_t i;

	if (!read_memory(pid, operation->attributes, &attributes,
			 sizeof(attributes)) ||
	    !read_object_id(pid, (int)attributes.fd, &operation->pinned))
		return;
	for (i = 0; i < sizeof(operation->pin_path); i++) {
		if (!read_memory(pid, attributes.pathname + i,
				 &operation->pin_path[i], 1))
			return;
		if (!operation->pin_path[i]) {
			operation->pin_valid = operation->pin_path[0] != 0;
			return;
		}
	}
}

static void resolve_pin(pid_t pid, struct operation *operation)
{
	char tracee_path[PATH_MAX + 64];
	struct stat observer_mount;
	struct stat tracee_mount;
	char namespace_path[64];
	char resolved[PATH_MAX];

	snprintf(namespace_path, sizeof(namespace_path), "/proc/%d/ns/mnt",
		 pid);
	if (stat("/proc/self/ns/mnt", &observer_mount) ||
	    stat(namespace_path, &tracee_mount) ||
	    observer_mount.st_dev != tracee_mount.st_dev ||
	    observer_mount.st_ino != tracee_mount.st_ino) {
		errno = ENOTSUP;
		fail("cannot verify a pin in a different mount namespace", pid);
	}
	if (operation->pin_path[0] == '/')
		snprintf(tracee_path, sizeof(tracee_path), "/proc/%d/root%s",
			 pid, operation->pin_path);
	else
		snprintf(tracee_path, sizeof(tracee_path), "/proc/%d/cwd/%s",
			 pid, operation->pin_path);
	if (!realpath(tracee_path, resolved))
		fail("resolve successful BPF pin", pid);
	strcpy(operation->pin_path, resolved);
}

static void write_json_string(const char *value)
{
	const unsigned char *p = (const unsigned char *)value;

	fputc('"', inventory);
	for (; *p; p++) {
		if (*p == '"' || *p == '\\')
			fprintf(inventory, "\\%c", *p);
		else if (*p < 32)
			fprintf(inventory, "\\u%04x", *p);
		else
			fputc(*p, inventory);
	}
	fputc('"', inventory);
}

static void capture_related_program(pid_t pid, struct operation *operation)
{
	unsigned int fd;

	if (!read_memory(pid, operation->attributes + 8, &fd, sizeof(fd)))
		return;
	read_object_id(pid, (int)fd, &operation->attachment_program);
}

static void
record_operation(pid_t pid, struct operation *operation, long result)
{
	struct object_id object = {0};
	bool created;

	if (result < 0 || operation->command == BPF_PROG_TEST_RUN)
		return;
	if (operation->command == BPF_OBJ_PIN) {
		if (!operation->pin_valid) {
			errno = ENOTSUP;
			fail("cannot identify successful pin", pid);
		}
		resolve_pin(pid, operation);
		fprintf(inventory,
			"{\"kind\":\"%s\",\"id\":%u,\"pid\":%d,\"created\":"
			"false,\"pin\":",
			operation->pinned.kind, operation->pinned.id, pid);
		write_json_string(operation->pin_path);
		fputs("}\n", inventory);
	} else {
		if (!read_object_id(pid, (int)result, &object)) {
			if (operation->command != BPF_RAW_TRACEPOINT_OPEN ||
			    errno != ENODATA) {
				errno = EPROTO;
				fail("cannot identify successful BPF FD", pid);
			}
		}
		if (operation->command == BPF_RAW_TRACEPOINT_OPEN &&
		    (!object.kind || strcmp(object.kind, "link"))) {
			if (!operation->attachment_program.id) {
				errno = EPROTO;
				fail("cannot identify legacy attachment "
				     "program",
				     pid);
			}
			fprintf(inventory,
				"{\"kind\":\"prog\",\"id\":%u,\"pid\":%d,"
				"\"created\":false,\"legacy_attachment\":true}"
				"\n",
				operation->attachment_program.id, pid);
			if (ferror(inventory) || fflush(inventory))
				fail("write legacy attachment identity", pid);
			record_count++;
			return;
		}
		if (object.program_type == OBSERVER_SYSCALL_PROGRAM) {
			errno = ENOTSUP;
			fail("syscall BPF programs can create unobserved "
			     "objects",
			     pid);
		}
		created = operation->command == BPF_MAP_CREATE ||
			  operation->command == BPF_PROG_LOAD ||
			  operation->command == OBSERVER_LINK_CREATE ||
			  (operation->command == BPF_RAW_TRACEPOINT_OPEN &&
			   !strcmp(object.kind, "link"));
		fprintf(inventory,
			"{\"kind\":\"%s\",\"id\":%u,\"pid\":%d,\"created\":%s}"
			"\n",
			object.kind, object.id, pid,
			created ? "true" : "false");
	}
	if (ferror(inventory) || fflush(inventory))
		fail("write BPF resource record", pid);
	record_count++;
}

static void observe_operations(void)
{
	struct traced_task *task;
	struct traced_task *event;
	struct operation operation;
	struct operation returned;
	bool syscall_exit;
	long result;
	int i;

	stop_other_tasks();
	for (;;) {
		task = NULL;
		for (i = 0; i < MAX_TASKS; i++)
			if (tasks[i].pid && tasks[i].operation) {
				task = &tasks[i];
				break;
			}
		if (!task)
			break;
		memset(&operation, 0, sizeof(operation));
		read_registers(task->pid, &operation, &result);
		if (operation.command == BPF_OBJ_PIN)
			capture_pin(task->pid, &operation);
		if (operation.command == BPF_RAW_TRACEPOINT_OPEN)
			capture_related_program(task->pid, &operation);
		if (operation.command == BPF_PROG_TEST_RUN) {
			struct object_id program = {0};
			unsigned int fd;

			if (!read_memory(task->pid, operation.attributes, &fd,
					 sizeof(fd)))
				fail("inspect test-run program FD", task->pid);
			if (read_object_id(task->pid, (int)fd, &program) &&
			    program.program_type == OBSERVER_SYSCALL_PROGRAM) {
				errno = ENOTSUP;
				fail("syscall BPF programs can create "
				     "unobserved objects",
				     task->pid);
			}
		}
		if (operation.command == BPF_PROG_LOAD) {
			unsigned int type;

			if (read_memory(task->pid, operation.attributes, &type,
					sizeof(type)) &&
			    type == OBSERVER_SYSCALL_PROGRAM) {
				errno = ENOTSUP;
				fail("syscall BPF programs can create "
				     "unobserved objects",
				     task->pid);
			}
		}
		resume_task(task, true);
		for (;;) {
			event = wait_event(&syscall_exit);
			if (!task->pid || task->exiting) {
				errno = EPROTO;
				fail("tracee exited during BPF operation",
				     root_pid);
			}
			if (event != task)
				continue;
			if (syscall_exit) {
				read_registers(task->pid, &returned, &result);
				record_operation(task->pid, &operation, result);
				task->operation = false;
				break;
			}
			resume_task(task, true);
		}
	}
	for (i = 0; i < MAX_TASKS; i++)
		if (tasks[i].pid && tasks[i].stopped)
			resume_task(&tasks[i], false);
}

#define TRACE_COMMAND(command)                                                 \
	BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, command, 0, 1),                    \
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_TRACE)

static void install_filter(void)
{
	struct sock_filter instructions[] = {
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
			 offsetof(struct seccomp_data, arch)),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, OBSERVER_ARCH, 1, 0),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | FOREIGN_SYSCALL_ABI),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
			 offsetof(struct seccomp_data, nr)),
#ifdef __x86_64__
		BPF_JUMP(BPF_JMP | BPF_JSET | BPF_K, 0x40000000, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | FOREIGN_SYSCALL_ABI),
#endif
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_clone, 0, 4),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
			 offsetof(struct seccomp_data, args[0])),
		BPF_JUMP(BPF_JMP | BPF_JSET | BPF_K, CLONE_UNTRACED, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | UNSUPPORTED_MUTATION),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, OBSERVER_CLONE3_SYSCALL, 0,
			 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | INSPECT_CLONE_FLAGS),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_seccomp, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | INSPECT_SECCOMP_COMMAND),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_prctl, 0, 4),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
			 offsetof(struct seccomp_data, args[0])),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, PR_SET_SECCOMP, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | UNSUPPORTED_MUTATION),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, SYS_bpf, 1, 0),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
		BPF_STMT(BPF_LD | BPF_W | BPF_ABS,
			 offsetof(struct seccomp_data, args[0])),
		TRACE_COMMAND(BPF_MAP_CREATE),
		TRACE_COMMAND(BPF_PROG_LOAD),
		TRACE_COMMAND(BPF_PROG_TEST_RUN),
		TRACE_COMMAND(BPF_OBJ_PIN),
		TRACE_COMMAND(BPF_OBJ_GET),
		TRACE_COMMAND(BPF_PROG_GET_FD_BY_ID),
		TRACE_COMMAND(BPF_MAP_GET_FD_BY_ID),
		TRACE_COMMAND(BPF_RAW_TRACEPOINT_OPEN),
		TRACE_COMMAND(OBSERVER_LINK_CREATE),
		TRACE_COMMAND(OBSERVER_LINK_GET_FD_BY_ID),
		/* Unsupported persistent mutations must not pass unobserved. */
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, BPF_PROG_ATTACH, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | UNSUPPORTED_MUTATION),
		BPF_JUMP(BPF_JMP | BPF_JEQ | BPF_K, OBSERVER_LINK_UPDATE, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | UNSUPPORTED_MUTATION),
		BPF_JUMP(BPF_JMP | BPF_JGE | BPF_K, 36, 0, 1),
		BPF_STMT(BPF_RET | BPF_K,
			 SECCOMP_RET_TRACE | UNSUPPORTED_MUTATION),
		BPF_STMT(BPF_RET | BPF_K, SECCOMP_RET_ALLOW),
	};
	struct sock_fprog filter = {
		.len = sizeof(instructions) / sizeof(instructions[0]),
		.filter = instructions,
	};

	if (prctl(PR_SET_SECCOMP, SECCOMP_MODE_FILTER, &filter) < 0)
		fail("install resource-operation filter", getpid());
}

int main(int argc, char **argv)
{
	unsigned long options = PTRACE_O_TRACESYSGOOD | PTRACE_O_TRACESECCOMP |
				PTRACE_O_TRACEFORK | PTRACE_O_TRACEVFORK |
				PTRACE_O_TRACECLONE | PTRACE_O_TRACEEXEC |
				PTRACE_O_TRACEEXIT | PTRACE_O_EXITKILL;
	sigset_t previous_signals;
	struct traced_task *task;
	char path[PATH_MAX];
	bool syscall_exit;
	int gate[2];
	char ignored;
	FILE *pid_file;

	if (argc < 4 || strcmp(argv[2], "--")) {
		fprintf(stderr, "usage: %s OUTPUT_DIR -- COMMAND [ARGS...]\n",
			argv[0]);
		return 2;
	}
	output_directory = argv[1];
	if (snprintf(path, sizeof(path), "%s/resources.jsonl",
		     output_directory) >= (int)sizeof(path)) {
		errno = ENAMETOOLONG;
		fail("observation output path", 0);
	}
	/* Reusing an old journal must never reuse a stale completion receipt.
	 */
	inventory = fopen(path, "wex");
	if (!inventory)
		fail("create a new observation journal", 0);
	atexit(write_completion);
	if (pipe2(gate, O_CLOEXEC) < 0)
		fail("open observation start gate", 0);
	sigemptyset(&observer_signals);
	sigaddset(&observer_signals, SIGTERM);
	sigaddset(&observer_signals, SIGINT);
	sigaddset(&observer_signals, SIGHUP);
	sigaddset(&observer_signals, SIGCHLD);
	if (sigprocmask(SIG_BLOCK, &observer_signals, &previous_signals))
		fail("block observer signals", 0);
	root_pid = fork();
	if (root_pid < 0)
		fail("fork observed command", 0);
	if (!root_pid) {
		close(gate[1]);
		if (read(gate[0], &ignored, 1) < 0)
			_exit(125);
		close(gate[0]);
		if (sigprocmask(SIG_SETMASK, &previous_signals, NULL))
			_exit(125);
		install_filter();
		execvp(argv[3], &argv[3]);
		perror("exec observed command");
		_exit(127);
	}
	close(gate[0]);
	if (ptrace(PTRACE_SEIZE, root_pid, NULL, (void *)options) < 0 ||
	    ptrace(PTRACE_INTERRUPT, root_pid, NULL, NULL) < 0)
		fail("attach before observed command starts", root_pid);
	task = wait_event(&syscall_exit);
	if (!task || task->pid != root_pid)
		fail("wait for initial trace stop", root_pid);
	snprintf(path, sizeof(path), "%s/agent.pid", output_directory);
	pid_file = fopen(path, "wex");
	if (!pid_file || fprintf(pid_file, "%d\n", root_pid) < 0 ||
	    fclose(pid_file))
		fail("publish observed process ID", root_pid);
	close(gate[1]);
	resume_task(task, false);
	while (tasks_remaining()) {
		task = wait_event(&syscall_exit);
		if (!task)
			continue;
		if (task->operation)
			observe_operations();
		else
			resume_task(task, false);
	}
	if (fclose(inventory))
		fail("close BPF resource record", root_pid);
	complete = true;
	return root_status;
}
