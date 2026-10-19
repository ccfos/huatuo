// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package bpfmock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the real fixture dispatchers with observable task/IRQ boundaries.
func TestFixtureCallingContexts(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("gcc is required")
	}
	for _, feature := range []string{"system_hungtask", "system_softlockup"} {
		t.Run(feature, func(t *testing.T) {
			dir := t.TempDir()
			linux := filepath.Join(dir, "linux")
			if err := os.Mkdir(linux, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, header := range []string{"fs", "miscdevice", "module", "uaccess", "sched", "workqueue", "kernel", "interrupt", "irq_work", "ktime", "preempt"} {
				if err := os.WriteFile(filepath.Join(linux, header+".h"), []byte(contextKernelStub), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			module, err := os.ReadFile(filepath.Join("..", feature, feature+"_mock_kmod.c"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "module.c"), module, 0o600); err != nil {
				t.Fatal(err)
			}
			header, err := os.ReadFile(filepath.Join("..", feature, feature+"_mock.h"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, feature+"_mock.h"), header, 0o600); err != nil {
				t.Fatal(err)
			}
			trace := `static struct task_struct *reported_task, *reporting_task;
static void trace_sched_process_hang_mock(struct task_struct *task) {
 reported_task=task; reporting_task=current;
}
`
			if err := os.WriteFile(filepath.Join(dir, "system_hungtask_trace.h"), []byte(trace), 0o600); err != nil {
				t.Fatal(err)
			}
			assertions := `struct hungtask_mock_request request={HUNGTASK_MOCK_CURRENT_TASK};
if(mock_ioctl(NULL,HUATUO_HUNGTASK_MOCK_RUN,(unsigned long)&request))return 1;
if(reported_task!=&caller || reporting_task!=&worker || reported_task==reporting_task)return 2;`
			if feature == "system_softlockup" {
				assertions = `struct softlockup_mock_request request={SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP};
if(mock_ioctl(NULL,HUATUO_SOFTLOCKUP_MOCK_RUN,(unsigned long)&request))return 1;
if(!irq_callbacks || preemption_disabled || current!=&caller)return 2;`
			}
			source := "#include \"module.c\"\nint main(void) {\n" + assertions + "\nreturn 0;\n}\n"
			path := filepath.Join(dir, "test.c")
			binary := filepath.Join(dir, "test")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, hard := range []bool{false, true} {
				args := []string{"-std=gnu11", "-I", dir, "-I", ".", path, "-o", binary}
				if hard {
					args = append(args, "-DTEST_HARD_IRQ")
				}
				if out, err := exec.Command(gcc, args...).CombinedOutput(); err != nil {
					t.Fatalf("compile: %v: %s", err, out)
				}
				if out, err := exec.Command(binary).CombinedOutput(); err != nil {
					t.Fatalf("calling context: %v: %s", err, out)
				}
			}
			// A current-task regression must fail even though the ioctl caller is valid.
			if feature == "system_hungtask" {
				mutant := strings.Replace(string(module), "trace_sched_process_hang_mock(report->task)", "trace_sched_process_hang_mock(current)", 1)
				if err := os.WriteFile(filepath.Join(dir, "module.c"), []byte(mutant), 0o600); err != nil {
					t.Fatal(err)
				}
				if out, err := exec.Command(gcc, "-std=gnu11", "-I", dir, "-I", ".", path, "-o", binary).CombinedOutput(); err != nil {
					t.Fatalf("compile mutant: %v: %s", err, out)
				}
				if err := exec.Command(binary).Run(); err == nil {
					t.Fatal("current-task regression escaped the fixture")
				}
			}
		})
	}
}

const contextKernelStub = `#ifndef TEST_KERNEL_H
#define TEST_KERNEL_H
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>
#include <errno.h>
#define CONFIG_TRACEPOINTS 1
#define CONFIG_EVENT_TRACING 1
#define CONFIG_PREEMPT_RT 0
#define CONFIG_PREEMPT_RT_FULL 0
#define IS_ENABLED(x) (x)
#define noinline __attribute__((noinline))
#define __noclone __attribute__((noclone))
#define __user
#define THIS_MODULE NULL
#define MISC_DYNAMIC_MINOR 0
#define MODULE_LICENSE(x)
#define MODULE_DESCRIPTION(x)
#define module_misc_device(x)
#define container_of(ptr,type,member) ((type *)((char *)(ptr)-offsetof(type,member)))
#define WRITE_ONCE(x,y) ((x)=(y))
#define READ_ONCE(x) (x)
#define NSEC_PER_SEC 1000000000ULL
typedef uint64_t u64;
struct file { int unused; };
struct file_operations { void *owner; long (*unlocked_ioctl)(struct file *,unsigned int,unsigned long); };
struct miscdevice { int minor; const char *name; const struct file_operations *fops; int mode; };
static int copy_from_user(void *dst,const void *src,size_t size) {memcpy(dst,src,size);return 0;}
struct task_struct { int pid; };
static struct task_struct caller={1},worker={2};
static struct task_struct *current=&caller;
struct work_struct { void (*func)(struct work_struct *); };
#define INIT_WORK(w,f) ((w)->func=(f))
static void schedule_work(struct work_struct *work) {current=&worker;work->func(work);current=&caller;}
static void flush_work(struct work_struct *work) {}
enum lockdep_ok { LOCKDEP_STILL_OK };
#define TAINT_SOFTLOCKUP 14
#define TAINT_USER 6
struct irq_work {void (*func)(struct irq_work *);};
#ifdef TEST_HARD_IRQ
#define IRQ_WORK_INIT_HARD(f) {.func=f}
#endif
static struct irq_work *pending_irq;
static int preemption_disabled,irq_callbacks,inside_irq;
static void init_irq_work(struct irq_work *work,void (*func)(struct irq_work *)) {work->func=func;}
static void preempt_disable(void) {preemption_disabled=1;}
static void preempt_enable(void) {preemption_disabled=0;}
static int hardirq_count(void) {return inside_irq;}
static void irq_work_queue(struct irq_work *work) {pending_irq=work;}
static void irq_work_sync(struct irq_work *work) {}
static u64 ktime_get_ns(void) {
 if(pending_irq) {
  struct irq_work *work=pending_irq;pending_irq=NULL;
  if(!preemption_disabled)__builtin_trap();
  inside_irq=1;irq_callbacks++;work->func(work);inside_irq=0;
 }
 return 0;
}
static void cpu_relax(void) {}
#endif
`
