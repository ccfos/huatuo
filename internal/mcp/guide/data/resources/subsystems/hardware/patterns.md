# Hardware root-cause patterns

## ras
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Corrected MCE | event=MCE and type=Corrected | Intermittent ECC memory errors; monitor trends |
| Uncorrected MCE | event=MCE and type=Uncorrected* | Possible DIMM failure requiring replacement |
| EDAC error | event=EDAC | Memory-controller error report; inspect severity and bit-error details |
| PCIe AER | event=AER | PCIe device or link error (NIC/GPU/NVMe) |
| ACPI/GHES | event=NON_STANDARD | Firmware-reported hardware error |
| MCE threshold event | event=MCE_THRESHOLD | MCE threshold interrupt storm may indicate impending hardware failure |

## lacp
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| One link down | A slave is down in content | Physical link or NIC failure |
| LACP negotiation failure | Repeated state changes in content | Switch configuration mismatch or peer without LACP support |
| All slaves down | All slaves are down in content | Switch-port or cabling failure |

## netdev_events
| Symptom | Signature | Typical cause |
|---------|-----------|---------------|
| Link flapping | Frequent linkstatus up/down transitions | Loose cable, faulty transceiver, or switch-port issue |
| Persistent link down | linkstatus=down without recovery | NIC hardware failure or disconnected cable |
| Reduced negotiated speed | Link is up at a lower speed | Autonegotiation failure or poor cable quality |
