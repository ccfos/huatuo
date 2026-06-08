#ifndef __BPF_COMMON_H__
#define __BPF_COMMON_H__

#ifndef NULL
#define NULL ((void *)0)
#endif

#ifndef __noinline
#define __noinline __attribute__((noinline))
#endif

/* define COMPAT_XXX for compat old kernel vmlinux.h */
#define COMPAT_BPF_F_CURRENT_CPU 0xffffffffULL

#define COMPAT_TASK_COMM_LEN   16
#define PATH_MAX        4096    /* # chars in a path name including nul */
#define COMPAT_CPU_NUM 128

/* include/uapi/linux/perf_event.h */
#define PERF_MAX_STACK_DEPTH	127
#define PERF_MIN_STACK_DEPTH	16
#define PERF_STACK_DEPTH	20

/* flags for both BPF_FUNC_get_stackid and BPF_FUNC_get_stack. */
#define COMPAT_BPF_F_USER_STACK 256

/* flags for BPF_MAP_UPDATE_ELEM command */
#define COMPAT_BPF_ANY		0 /* create new element or update existing */
#define COMPAT_BPF_NOEXIST	1 /* create new element if it didn't exist */
#define COMPAT_BPF_EXIST	2 /* update existing element */
#define COMPAT_BPF_F_LOCK	4 /* spin_lock-ed map_lookup/map_update */

#define NR_SOFTIRQS_MAX 16

#define NSEC_PER_MSEC 1000000UL
#define NSEC_PER_USEC 1000UL

#endif /* __BPF_COMMON_H__ */
