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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Compile the actual module sources with observable hook calls and allocations.
func TestNativeMemoryModuleScenarios(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("gcc is required")
	}
	for _, feature := range []string{"native_physical_alloc", "native_physical_usage", "native_virtual_alloc"} {
		for _, folio := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/folio=%t", feature, folio), func(t *testing.T) {
				dir := t.TempDir()
				linux := filepath.Join(dir, "linux")
				if err := os.Mkdir(linux, 0o700); err != nil {
					t.Fatal(err)
				}
				stub := `#ifndef TEST_KERNEL_H
#define TEST_KERNEL_H
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <errno.h>
#include <stdint.h>
#include <sys/ioctl.h>
typedef uint32_t __u32;
#define __user
#define __init
#define __exit
#define THIS_MODULE NULL
#define MISC_DYNAMIC_MINOR 0
#define GFP_KERNEL 0
#define __GFP_ZERO 0
#define __GFP_COMP 0
#define module_misc_device(x)
#define module_init(x)
#define module_exit(x)
#define MODULE_LICENSE(x)
#define MODULE_DESCRIPTION(x)
struct file { int value; };
struct file_operations { void *owner; long (*unlocked_ioctl)(struct file *,unsigned int,unsigned long); };
struct miscdevice { int minor; const char *name; const struct file_operations *fops; int mode; };
static int misc_register(struct miscdevice *d) { return 0; }
static void misc_deregister(struct miscdevice *d) {}
struct page { int order, offset; };
struct folio;
static int fail_copy, fail_alloc, live_allocations, allocations, writes;
static char calls[1024];
static void record(const char *operation,int order,int count) {
 char item[64]; snprintf(item,sizeof(item),"%s%d:%d;",operation,order,count);strcat(calls,item);
}
static struct page *alloc_pages(int flags,int order) {
 struct page *pages;int i;
 if (++allocations == fail_alloc) return NULL;
 pages=calloc(1<<order,sizeof(*pages));
 for(i=0;i<(1<<order);i++){pages[i].order=order;pages[i].offset=i;}
 live_allocations++;return pages;
}
static void __free_pages(struct page *p,int order) {live_allocations--;free(p);}
static void *kzalloc(size_t size,int flags) {live_allocations++;return calloc(1,size);}
static void kfree(void *p) {live_allocations--;free(p);}
static int copy_from_user(void *dst,const void *src,size_t length) {if(fail_copy)return 1;memcpy(dst,src,length);return 0;}
#endif
`
				for _, name := range []string{"fs.h", "miscdevice.h", "module.h", "uaccess.h", "mm.h", "rmap.h", "slab.h"} {
					if err := os.WriteFile(filepath.Join(linux, name), []byte(stub), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(linux, "types.h"), []byte("#include \"mm.h\"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(linux, "ioctl.h"), []byte("#include <asm-generic/ioctl.h>\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				hooks := `#define CALL_page_add_new_anon_rmap(p) record("A",(p)->order,1)
#define CALL_page_remove_rmap(p) record("F",(p)->order,1)
#define CALL_do_mmap(file,addr,len) do { if(file) record("M",0,(int)(len));else record("N",0,(int)(len)); } while(0)
`
				if folio {
					hooks += `#define HAVE_folio_add_new_anon_rmap 1
#define HAVE_folio_remove_rmap_ptes 1
#define CALL_folio_add_new_anon_rmap(p) record("A",((struct page *)(p))->order,1)
#define CALL_folio_remove_rmap_ptes(p,page,count) record("F",((struct page *)(p))->order,count)
`
				}
				if err := os.WriteFile(filepath.Join(dir, feature+"_hook.h"), []byte(hooks), 0o600); err != nil {
					t.Fatal(err)
				}
				sourcePath, err := filepath.Abs(filepath.Join("..", feature, feature+"_mock_kmod.c"))
				if err != nil {
					t.Fatal(err)
				}
				// The source's quoted header must resolve to the real request definition.
				prefix := strings.ToUpper(feature)
				expected := []string{}
				switch feature {
				case "native_physical_alloc":
					if folio {
						expected = []string{"A0:1;A2:1;"}
					} else {
						expected = []string{"A0:1;A2:1;A2:1;A2:1;A2:1;"}
					}
				case "native_physical_usage":
					if folio {
						expected = []string{"A2:1;A0:1;", "A2:1;A0:1;F0:1;F2:4;F2:4;", "A2:1;A0:1;F0:1;F2:1;F2:1;", "F0:1;"}
					} else {
						alloc := "A2:1;A2:1;A2:1;A2:1;A0:1;"
						expected = []string{alloc, alloc + "F0:1;" + strings.Repeat("F2:1;", 8), alloc + "F0:1;F2:1;F2:1;", "F0:1;"}
					}
				case "native_virtual_alloc":
					expected = []string{"M0:16384;N0:4096;N0:8192;N0:0;", "M0:16384;"}
				}
				checks := ""
				for i, want := range expected {
					checks += fmt.Sprintf("request.scenario=%d;calls[0]=0;allocations=0;if(mock_ioctl(NULL,%s_RUN,(unsigned long)&request)||strcmp(calls,%q)||live_allocations){fprintf(stderr,\"scenario %d: %%s live=%%d\\n\",calls,live_allocations);return 1;}\n", i, prefix, want, i)
				}
				code := fmt.Sprintf(`#include %q
int main(void) {
 struct %s_request request={0};
 if(mock_ioctl(NULL,0,(unsigned long)&request)!=-ENOTTY)return 2;
 fail_copy=1;if(mock_ioctl(NULL,%s_RUN,(unsigned long)&request)!=-EFAULT)return 3;fail_copy=0;
 request.scenario=100;if(mock_ioctl(NULL,%s_RUN,(unsigned long)&request)!=-EINVAL)return 4;
 %s
 return 0;
}
`, sourcePath, feature, prefix, prefix, checks)
				path := filepath.Join(dir, "test.c")
				binary := filepath.Join(dir, "test")
				if err := os.WriteFile(path, []byte(code), 0o600); err != nil {
					t.Fatal(err)
				}
				output, err := exec.Command(gcc, "-std=gnu11", "-I", ".", "-I", dir, path, "-o", binary).CombinedOutput()
				if err != nil {
					t.Fatalf("compile: %v: %s", err, output)
				}
				output, err = exec.Command(binary).CombinedOutput()
				if err != nil {
					t.Fatalf("scenarios: %v: %s", err, output)
				}
			})
		}
	}
}
