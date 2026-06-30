#ifndef __BPF_MAP_H__
#define __BPF_MAP_H__

/* Mirror of include/uapi/linux/bpf.h BPF_ANY family for vmlinux.h that
 * predates BPF_F_CPU / BPF_F_ALL_CPUS. Guarded so newer vmlinux.h wins. */
#ifndef BPF_ANY
enum {
	BPF_ANY		= 0,  /* create new element or update existing */
	BPF_NOEXIST	= 1,  /* create new element if it didn't exist */
	BPF_EXIST	= 2,  /* update existing element */
	BPF_F_LOCK	= 4,  /* spin_lock-ed map_lookup/map_update */
	BPF_F_CPU	= 8,  /* cpu flag for percpu maps, upper 32-bit of flags is a cpu number */
	BPF_F_ALL_CPUS	= 16, /* update value across all CPUs for percpu maps */
};
#endif

#endif /* __BPF_MAP_H__ */
