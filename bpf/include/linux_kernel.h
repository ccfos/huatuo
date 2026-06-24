#ifndef __LINUX_KERNEL_H__
#define __LINUX_KERNEL_H__

// copy from include/linux/kernel.h

/* This cannot be an enum because some may be used in assembly source. */
#define TAINT_PROPRIETARY_MODULE        0
#define TAINT_FORCED_MODULE             1
#define TAINT_CPU_OUT_OF_SPEC           2
#define TAINT_FORCED_RMMOD              3
#define TAINT_MACHINE_CHECK             4
#define TAINT_BAD_PAGE                  5
#define TAINT_USER                      6
#define TAINT_DIE                       7
#define TAINT_OVERRIDDEN_ACPI_TABLE     8
#define TAINT_WARN                      9
#define TAINT_CRAP                      10
#define TAINT_FIRMWARE_WORKAROUND       11
#define TAINT_OOT_MODULE                12
#define TAINT_UNSIGNED_MODULE           13
#define TAINT_SOFTLOCKUP                14
#define TAINT_LIVEPATCH                 15
#define TAINT_AUX                       16
#define TAINT_RANDSTRUCT                17
#define TAINT_18                        18
#define TAINT_19                        19
#define TAINT_20                        20
#define TAINT_21                        21
#define TAINT_22                        22
#define TAINT_23                        23
#define TAINT_24                        24
#define TAINT_25                        25
#define TAINT_26                        26
/* Start of Red Hat-specific taint flags */
#define TAINT_SUPPORT_REMOVED           27
#define TAINT_28                        28
#define TAINT_TECH_PREVIEW              29
#define TAINT_UNPRIVILEGED_BPF          30
#define TAINT_31                        31
/* End of Red Hat-specific taint flags */
#define TAINT_FLAGS_COUNT               32


#endif
