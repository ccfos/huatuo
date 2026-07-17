// SPDX-License-Identifier: Apache-2.0

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

#define HUATUO_LOCKPROF_IOC_MAGIC 0xb7
#define HUATUO_LOCKPROF_MUTEX _IO(HUATUO_LOCKPROF_IOC_MAGIC, 1)
#define HUATUO_LOCKPROF_SPIN _IO(HUATUO_LOCKPROF_IOC_MAGIC, 2)
#define HUATUO_LOCKPROF_RW_READ _IO(HUATUO_LOCKPROF_IOC_MAGIC, 3)
#define HUATUO_LOCKPROF_RW_WRITE _IO(HUATUO_LOCKPROF_IOC_MAGIC, 4)
#define WORKER_COUNT 8

static volatile sig_atomic_t stopping;
static volatile sig_atomic_t failed;
static volatile sig_atomic_t failure_errno;

struct worker_arg {
	int fd;
	unsigned int command;
};

static void stop_workload(int signo)
{
	(void)signo;
	stopping = 1;
}

static void *worker(void *opaque)
{
	const struct worker_arg *arg = opaque;

	while (!stopping) {
		if (ioctl(arg->fd, arg->command, 0) == 0)
			continue;
		if (errno == EINTR)
			continue;
		failure_errno = errno;
		failed = 1;
		stopping = 1;
	}
	return NULL;
}

static unsigned int command_for(const char *lock_type, int worker_id)
{
	if (strcmp(lock_type, "mutex") == 0)
		return HUATUO_LOCKPROF_MUTEX;
	if (strcmp(lock_type, "spinlock") == 0)
		return HUATUO_LOCKPROF_SPIN;
	if (strcmp(lock_type, "rwlock") == 0)
		return worker_id % 2 == 0 ? HUATUO_LOCKPROF_RW_READ :
					   HUATUO_LOCKPROF_RW_WRITE;
	return 0;
}

int main(int argc, char **argv)
{
	pthread_t threads[WORKER_COUNT];
	struct worker_arg args[WORKER_COUNT];
	struct sigaction action = {.sa_handler = stop_workload};
	int fd;
	int i;

	if (argc != 3) {
		fprintf(stderr, "usage: %s <device> <mutex|spinlock|rwlock>\n", argv[0]);
		return 2;
	}
	if (command_for(argv[2], 0) == 0) {
		fprintf(stderr, "unsupported lock type: %s\n", argv[2]);
		return 2;
	}

	sigemptyset(&action.sa_mask);
	sigaction(SIGTERM, &action, NULL);
	sigaction(SIGINT, &action, NULL);

	fd = open(argv[1], O_RDONLY | O_CLOEXEC);
	if (fd < 0) {
		perror("open fixture device");
		return 1;
	}

	for (i = 0; i < WORKER_COUNT; i++) {
		args[i].fd = fd;
		args[i].command = command_for(argv[2], i);
		if (pthread_create(&threads[i], NULL, worker, &args[i]) != 0) {
			perror("pthread_create");
			stopping = 1;
			failed = 1;
			break;
		}
	}

	while (!stopping)
		sleep(1);
	for (int joined = 0; joined < i; joined++)
		pthread_join(threads[joined], NULL);
	close(fd);
	if (failed && failure_errno != 0)
		fprintf(stderr, "ioctl failed: %s\n", strerror(failure_errno));
	return failed ? 1 : 0;
}
