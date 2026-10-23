// SPDX-License-Identifier: GPL-2.0-only
// Copyright 2026 The HuaTuo Authors

#include <linux/etherdevice.h>
#include <linux/module.h>
#include <linux/netdevice.h>
#include <linux/platform_device.h>

#define DRIVER_NAME "ht_tx_timeout_t"

static char *ifname = "httx%d";
module_param(ifname, charp, 0444);
MODULE_PARM_DESC(ifname, "Test network device name");

static struct net_device *test_dev;
static struct platform_device *test_parent;
static atomic_t timeout_count = ATOMIC_INIT(0);

static int test_open(struct net_device *dev)
{
	/* Opening the device injects one stall; the watchdog callback recovers it. */
	netif_trans_update(dev);
	netif_stop_queue(dev);
	netif_carrier_on(dev);
	return 0;
}

static int test_stop(struct net_device *dev)
{
	netif_stop_queue(dev);
	netif_carrier_off(dev);
	return 0;
}

static netdev_tx_t test_xmit(struct sk_buff *skb, struct net_device *dev)
{
	dev_kfree_skb(skb);
	return NETDEV_TX_OK;
}

static void test_timeout(struct net_device *dev, unsigned int queue)
{
	atomic_inc(&timeout_count);
	netif_wake_queue(dev);
}

static void test_timeout_legacy(struct net_device *dev)
{
	test_timeout(dev, 0);
}

static const struct net_device_ops test_netdev_ops = {
	.ndo_open = test_open,
	.ndo_stop = test_stop,
	.ndo_start_xmit = test_xmit,
	/* Enterprise kernels may backport the queue argument independently. */
	.ndo_tx_timeout = __builtin_choose_expr(
		__builtin_types_compatible_p(
			typeof(((const struct net_device_ops *)0)->ndo_tx_timeout),
			void (*)(struct net_device *, unsigned int)),
		test_timeout, test_timeout_legacy),
};

static void test_setup(struct net_device *dev)
{
	ether_setup(dev);
	eth_hw_addr_random(dev);
	dev->netdev_ops = &test_netdev_ops;
	dev->watchdog_timeo = HZ;
	/* A real qdisc is required for the watchdog to inspect this queue. */
	dev->tx_queue_len = 16;
	dev->flags |= IFF_NOARP;
	dev->flags &= ~IFF_MULTICAST;
	dev->priv_flags &= ~IFF_NO_QUEUE;
	netif_carrier_off(dev);
}

static int timeout_count_get(char *buffer, const struct kernel_param *kp)
{
	return scnprintf(buffer, PAGE_SIZE, "%d\n", atomic_read(&timeout_count));
}

static const struct kernel_param_ops timeout_count_ops = {
	.get = timeout_count_get,
};
module_param_cb(timeout_count, &timeout_count_ops, NULL, 0400);

static int test_probe(struct platform_device *pdev)
{
	return 0;
}

/* The tracepoint resolves driver_name from the netdev's parent driver. */
static struct platform_driver test_driver = {
	.probe = test_probe,
	.driver.name = DRIVER_NAME,
};

static int __init test_init(void)
{
	int err;

	err = platform_driver_register(&test_driver);
	if (err)
		return err;
	test_parent = platform_device_register_simple(DRIVER_NAME, -1, NULL, 0);
	if (IS_ERR(test_parent)) {
		err = PTR_ERR(test_parent);
		goto unregister_driver;
	}

	test_dev = alloc_netdev(0, ifname, NET_NAME_UNKNOWN, test_setup);
	if (!test_dev) {
		err = -ENOMEM;
		goto unregister_parent;
	}
	SET_NETDEV_DEV(test_dev, &test_parent->dev);
	err = register_netdev(test_dev);
	if (err)
		goto free_dev;

	return 0;

free_dev:
	free_netdev(test_dev);
unregister_parent:
	platform_device_unregister(test_parent);
unregister_driver:
	platform_driver_unregister(&test_driver);
	return err;
}

static void __exit test_exit(void)
{
	unregister_netdev(test_dev);
	free_netdev(test_dev);
	platform_device_unregister(test_parent);
	platform_driver_unregister(&test_driver);
}

module_init(test_init);
module_exit(test_exit);
MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Single-queue watchdog fixture for HuaTuo integration tests");
