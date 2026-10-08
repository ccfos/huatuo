obj-m += huatuo_oom_mock.o

KERNEL_BUILD_DIR ?= /lib/modules/$(shell uname -r)/build

all:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) modules

clean:
	$(MAKE) -C $(KERNEL_BUILD_DIR) M=$(CURDIR) clean
