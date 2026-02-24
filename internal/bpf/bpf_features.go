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

package bpf

import (
	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/asm"
	"github.com/cilium/ebpf/features"
)

type (
	BpfProgramType = ebpf.ProgramType
	BpfBuiltinFunc = asm.BuiltinFunc
)

func ProgramProbe(pt BpfProgramType, helper BpfBuiltinFunc) error {
	return features.HaveProgramHelper(pt, helper)
}

// eBPF program types (Linux).
const (
	UnspecifiedProgram    BpfProgramType = ebpf.UnspecifiedProgram
	SocketFilter          BpfProgramType = ebpf.SocketFilter
	Kprobe                BpfProgramType = ebpf.Kprobe
	SchedCLS              BpfProgramType = ebpf.SchedCLS
	SchedACT              BpfProgramType = ebpf.SchedACT
	TracePoint            BpfProgramType = ebpf.TracePoint
	XDP                   BpfProgramType = ebpf.XDP
	PerfEvent             BpfProgramType = ebpf.PerfEvent
	CGroupSKB             BpfProgramType = ebpf.CGroupSKB
	CGroupSock            BpfProgramType = ebpf.CGroupSock
	LWTIn                 BpfProgramType = ebpf.LWTIn
	LWTOut                BpfProgramType = ebpf.LWTOut
	LWTXmit               BpfProgramType = ebpf.LWTXmit
	SockOps               BpfProgramType = ebpf.SockOps
	SkSKB                 BpfProgramType = ebpf.SkSKB
	CGroupDevice          BpfProgramType = ebpf.CGroupDevice
	SkMsg                 BpfProgramType = ebpf.SkMsg
	RawTracepoint         BpfProgramType = ebpf.RawTracepoint
	CGroupSockAddr        BpfProgramType = ebpf.CGroupSockAddr
	LWTSeg6Local          BpfProgramType = ebpf.LWTSeg6Local
	LircMode2             BpfProgramType = ebpf.LircMode2
	SkReuseport           BpfProgramType = ebpf.SkReuseport
	FlowDissector         BpfProgramType = ebpf.FlowDissector
	CGroupSysctl          BpfProgramType = ebpf.CGroupSysctl
	RawTracepointWritable BpfProgramType = ebpf.RawTracepointWritable
	CGroupSockopt         BpfProgramType = ebpf.CGroupSockopt
	Tracing               BpfProgramType = ebpf.Tracing
	StructOps             BpfProgramType = ebpf.StructOps
	Extension             BpfProgramType = ebpf.Extension
	LSM                   BpfProgramType = ebpf.LSM
	SkLookup              BpfProgramType = ebpf.SkLookup
	Syscall               BpfProgramType = ebpf.Syscall
	Netfilter             BpfProgramType = ebpf.Netfilter
)

// Built-in functions (Linux).
const (
	FnUnspec                     BpfBuiltinFunc = asm.FnUnspec
	FnMapLookupElem              BpfBuiltinFunc = asm.FnMapLookupElem
	FnMapUpdateElem              BpfBuiltinFunc = asm.FnMapUpdateElem
	FnMapDeleteElem              BpfBuiltinFunc = asm.FnMapDeleteElem
	FnProbeRead                  BpfBuiltinFunc = asm.FnProbeRead
	FnKtimeGetNs                 BpfBuiltinFunc = asm.FnKtimeGetNs
	FnTracePrintk                BpfBuiltinFunc = asm.FnTracePrintk
	FnGetPrandomU32              BpfBuiltinFunc = asm.FnGetPrandomU32
	FnGetSmpProcessorId          BpfBuiltinFunc = asm.FnGetSmpProcessorId
	FnSkbStoreBytes              BpfBuiltinFunc = asm.FnSkbStoreBytes
	FnL3CsumReplace              BpfBuiltinFunc = asm.FnL3CsumReplace
	FnL4CsumReplace              BpfBuiltinFunc = asm.FnL4CsumReplace
	FnTailCall                   BpfBuiltinFunc = asm.FnTailCall
	FnCloneRedirect              BpfBuiltinFunc = asm.FnCloneRedirect
	FnGetCurrentPidTgid          BpfBuiltinFunc = asm.FnGetCurrentPidTgid
	FnGetCurrentUidGid           BpfBuiltinFunc = asm.FnGetCurrentUidGid
	FnGetCurrentComm             BpfBuiltinFunc = asm.FnGetCurrentComm
	FnGetCgroupClassid           BpfBuiltinFunc = asm.FnGetCgroupClassid
	FnSkbVlanPush                BpfBuiltinFunc = asm.FnSkbVlanPush
	FnSkbVlanPop                 BpfBuiltinFunc = asm.FnSkbVlanPop
	FnSkbGetTunnelKey            BpfBuiltinFunc = asm.FnSkbGetTunnelKey
	FnSkbSetTunnelKey            BpfBuiltinFunc = asm.FnSkbSetTunnelKey
	FnPerfEventRead              BpfBuiltinFunc = asm.FnPerfEventRead
	FnRedirect                   BpfBuiltinFunc = asm.FnRedirect
	FnGetRouteRealm              BpfBuiltinFunc = asm.FnGetRouteRealm
	FnPerfEventOutput            BpfBuiltinFunc = asm.FnPerfEventOutput
	FnSkbLoadBytes               BpfBuiltinFunc = asm.FnSkbLoadBytes
	FnGetStackid                 BpfBuiltinFunc = asm.FnGetStackid
	FnCsumDiff                   BpfBuiltinFunc = asm.FnCsumDiff
	FnSkbGetTunnelOpt            BpfBuiltinFunc = asm.FnSkbGetTunnelOpt
	FnSkbSetTunnelOpt            BpfBuiltinFunc = asm.FnSkbSetTunnelOpt
	FnSkbChangeProto             BpfBuiltinFunc = asm.FnSkbChangeProto
	FnSkbChangeType              BpfBuiltinFunc = asm.FnSkbChangeType
	FnSkbUnderCgroup             BpfBuiltinFunc = asm.FnSkbUnderCgroup
	FnGetHashRecalc              BpfBuiltinFunc = asm.FnGetHashRecalc
	FnGetCurrentTask             BpfBuiltinFunc = asm.FnGetCurrentTask
	FnProbeWriteUser             BpfBuiltinFunc = asm.FnProbeWriteUser
	FnCurrentTaskUnderCgroup     BpfBuiltinFunc = asm.FnCurrentTaskUnderCgroup
	FnSkbChangeTail              BpfBuiltinFunc = asm.FnSkbChangeTail
	FnSkbPullData                BpfBuiltinFunc = asm.FnSkbPullData
	FnCsumUpdate                 BpfBuiltinFunc = asm.FnCsumUpdate
	FnSetHashInvalid             BpfBuiltinFunc = asm.FnSetHashInvalid
	FnGetNumaNodeId              BpfBuiltinFunc = asm.FnGetNumaNodeId
	FnSkbChangeHead              BpfBuiltinFunc = asm.FnSkbChangeHead
	FnXdpAdjustHead              BpfBuiltinFunc = asm.FnXdpAdjustHead
	FnProbeReadStr               BpfBuiltinFunc = asm.FnProbeReadStr
	FnGetSocketCookie            BpfBuiltinFunc = asm.FnGetSocketCookie
	FnGetSocketUid               BpfBuiltinFunc = asm.FnGetSocketUid
	FnSetHash                    BpfBuiltinFunc = asm.FnSetHash
	FnSetsockopt                 BpfBuiltinFunc = asm.FnSetsockopt
	FnSkbAdjustRoom              BpfBuiltinFunc = asm.FnSkbAdjustRoom
	FnRedirectMap                BpfBuiltinFunc = asm.FnRedirectMap
	FnSkRedirectMap              BpfBuiltinFunc = asm.FnSkRedirectMap
	FnSockMapUpdate              BpfBuiltinFunc = asm.FnSockMapUpdate
	FnXdpAdjustMeta              BpfBuiltinFunc = asm.FnXdpAdjustMeta
	FnPerfEventReadValue         BpfBuiltinFunc = asm.FnPerfEventReadValue
	FnPerfProgReadValue          BpfBuiltinFunc = asm.FnPerfProgReadValue
	FnGetsockopt                 BpfBuiltinFunc = asm.FnGetsockopt
	FnOverrideReturn             BpfBuiltinFunc = asm.FnOverrideReturn
	FnSockOpsCbFlagsSet          BpfBuiltinFunc = asm.FnSockOpsCbFlagsSet
	FnMsgRedirectMap             BpfBuiltinFunc = asm.FnMsgRedirectMap
	FnMsgApplyBytes              BpfBuiltinFunc = asm.FnMsgApplyBytes
	FnMsgCorkBytes               BpfBuiltinFunc = asm.FnMsgCorkBytes
	FnMsgPullData                BpfBuiltinFunc = asm.FnMsgPullData
	FnBind                       BpfBuiltinFunc = asm.FnBind
	FnXdpAdjustTail              BpfBuiltinFunc = asm.FnXdpAdjustTail
	FnSkbGetXfrmState            BpfBuiltinFunc = asm.FnSkbGetXfrmState
	FnGetStack                   BpfBuiltinFunc = asm.FnGetStack
	FnSkbLoadBytesRelative       BpfBuiltinFunc = asm.FnSkbLoadBytesRelative
	FnFibLookup                  BpfBuiltinFunc = asm.FnFibLookup
	FnSockHashUpdate             BpfBuiltinFunc = asm.FnSockHashUpdate
	FnMsgRedirectHash            BpfBuiltinFunc = asm.FnMsgRedirectHash
	FnSkRedirectHash             BpfBuiltinFunc = asm.FnSkRedirectHash
	FnLwtPushEncap               BpfBuiltinFunc = asm.FnLwtPushEncap
	FnLwtSeg6StoreBytes          BpfBuiltinFunc = asm.FnLwtSeg6StoreBytes
	FnLwtSeg6AdjustSrh           BpfBuiltinFunc = asm.FnLwtSeg6AdjustSrh
	FnLwtSeg6Action              BpfBuiltinFunc = asm.FnLwtSeg6Action
	FnRcRepeat                   BpfBuiltinFunc = asm.FnRcRepeat
	FnRcKeydown                  BpfBuiltinFunc = asm.FnRcKeydown
	FnSkbCgroupId                BpfBuiltinFunc = asm.FnSkbCgroupId
	FnGetCurrentCgroupId         BpfBuiltinFunc = asm.FnGetCurrentCgroupId
	FnGetLocalStorage            BpfBuiltinFunc = asm.FnGetLocalStorage
	FnSkSelectReuseport          BpfBuiltinFunc = asm.FnSkSelectReuseport
	FnSkbAncestorCgroupId        BpfBuiltinFunc = asm.FnSkbAncestorCgroupId
	FnSkLookupTcp                BpfBuiltinFunc = asm.FnSkLookupTcp
	FnSkLookupUdp                BpfBuiltinFunc = asm.FnSkLookupUdp
	FnSkRelease                  BpfBuiltinFunc = asm.FnSkRelease
	FnMapPushElem                BpfBuiltinFunc = asm.FnMapPushElem
	FnMapPopElem                 BpfBuiltinFunc = asm.FnMapPopElem
	FnMapPeekElem                BpfBuiltinFunc = asm.FnMapPeekElem
	FnMsgPushData                BpfBuiltinFunc = asm.FnMsgPushData
	FnMsgPopData                 BpfBuiltinFunc = asm.FnMsgPopData
	FnRcPointerRel               BpfBuiltinFunc = asm.FnRcPointerRel
	FnSpinLock                   BpfBuiltinFunc = asm.FnSpinLock
	FnSpinUnlock                 BpfBuiltinFunc = asm.FnSpinUnlock
	FnSkFullsock                 BpfBuiltinFunc = asm.FnSkFullsock
	FnTcpSock                    BpfBuiltinFunc = asm.FnTcpSock
	FnSkbEcnSetCe                BpfBuiltinFunc = asm.FnSkbEcnSetCe
	FnGetListenerSock            BpfBuiltinFunc = asm.FnGetListenerSock
	FnSkcLookupTcp               BpfBuiltinFunc = asm.FnSkcLookupTcp
	FnTcpCheckSyncookie          BpfBuiltinFunc = asm.FnTcpCheckSyncookie
	FnSysctlGetName              BpfBuiltinFunc = asm.FnSysctlGetName
	FnSysctlGetCurrentValue      BpfBuiltinFunc = asm.FnSysctlGetCurrentValue
	FnSysctlGetNewValue          BpfBuiltinFunc = asm.FnSysctlGetNewValue
	FnSysctlSetNewValue          BpfBuiltinFunc = asm.FnSysctlSetNewValue
	FnStrtol                     BpfBuiltinFunc = asm.FnStrtol
	FnStrtoul                    BpfBuiltinFunc = asm.FnStrtoul
	FnSkStorageGet               BpfBuiltinFunc = asm.FnSkStorageGet
	FnSkStorageDelete            BpfBuiltinFunc = asm.FnSkStorageDelete
	FnSendSignal                 BpfBuiltinFunc = asm.FnSendSignal
	FnTcpGenSyncookie            BpfBuiltinFunc = asm.FnTcpGenSyncookie
	FnSkbOutput                  BpfBuiltinFunc = asm.FnSkbOutput
	FnProbeReadUser              BpfBuiltinFunc = asm.FnProbeReadUser
	FnProbeReadKernel            BpfBuiltinFunc = asm.FnProbeReadKernel
	FnProbeReadUserStr           BpfBuiltinFunc = asm.FnProbeReadUserStr
	FnProbeReadKernelStr         BpfBuiltinFunc = asm.FnProbeReadKernelStr
	FnTcpSendAck                 BpfBuiltinFunc = asm.FnTcpSendAck
	FnSendSignalThread           BpfBuiltinFunc = asm.FnSendSignalThread
	FnJiffies64                  BpfBuiltinFunc = asm.FnJiffies64
	FnReadBranchRecords          BpfBuiltinFunc = asm.FnReadBranchRecords
	FnGetNsCurrentPidTgid        BpfBuiltinFunc = asm.FnGetNsCurrentPidTgid
	FnXdpOutput                  BpfBuiltinFunc = asm.FnXdpOutput
	FnGetNetnsCookie             BpfBuiltinFunc = asm.FnGetNetnsCookie
	FnGetCurrentAncestorCgroupId BpfBuiltinFunc = asm.FnGetCurrentAncestorCgroupId
	FnSkAssign                   BpfBuiltinFunc = asm.FnSkAssign
	FnKtimeGetBootNs             BpfBuiltinFunc = asm.FnKtimeGetBootNs
	FnSeqPrintf                  BpfBuiltinFunc = asm.FnSeqPrintf
	FnSeqWrite                   BpfBuiltinFunc = asm.FnSeqWrite
	FnSkCgroupId                 BpfBuiltinFunc = asm.FnSkCgroupId
	FnSkAncestorCgroupId         BpfBuiltinFunc = asm.FnSkAncestorCgroupId
	FnRingbufOutput              BpfBuiltinFunc = asm.FnRingbufOutput
	FnRingbufReserve             BpfBuiltinFunc = asm.FnRingbufReserve
	FnRingbufSubmit              BpfBuiltinFunc = asm.FnRingbufSubmit
	FnRingbufDiscard             BpfBuiltinFunc = asm.FnRingbufDiscard
	FnRingbufQuery               BpfBuiltinFunc = asm.FnRingbufQuery
	FnCsumLevel                  BpfBuiltinFunc = asm.FnCsumLevel
	FnSkcToTcp6Sock              BpfBuiltinFunc = asm.FnSkcToTcp6Sock
	FnSkcToTcpSock               BpfBuiltinFunc = asm.FnSkcToTcpSock
	FnSkcToTcpTimewaitSock       BpfBuiltinFunc = asm.FnSkcToTcpTimewaitSock
	FnSkcToTcpRequestSock        BpfBuiltinFunc = asm.FnSkcToTcpRequestSock
	FnSkcToUdp6Sock              BpfBuiltinFunc = asm.FnSkcToUdp6Sock
	FnGetTaskStack               BpfBuiltinFunc = asm.FnGetTaskStack
	FnLoadHdrOpt                 BpfBuiltinFunc = asm.FnLoadHdrOpt
	FnStoreHdrOpt                BpfBuiltinFunc = asm.FnStoreHdrOpt
	FnReserveHdrOpt              BpfBuiltinFunc = asm.FnReserveHdrOpt
	FnInodeStorageGet            BpfBuiltinFunc = asm.FnInodeStorageGet
	FnInodeStorageDelete         BpfBuiltinFunc = asm.FnInodeStorageDelete
	FnDPath                      BpfBuiltinFunc = asm.FnDPath
	FnCopyFromUser               BpfBuiltinFunc = asm.FnCopyFromUser
	FnSnprintfBtf                BpfBuiltinFunc = asm.FnSnprintfBtf
	FnSeqPrintfBtf               BpfBuiltinFunc = asm.FnSeqPrintfBtf
	FnSkbCgroupClassid           BpfBuiltinFunc = asm.FnSkbCgroupClassid
	FnRedirectNeigh              BpfBuiltinFunc = asm.FnRedirectNeigh
	FnPerCpuPtr                  BpfBuiltinFunc = asm.FnPerCpuPtr
	FnThisCpuPtr                 BpfBuiltinFunc = asm.FnThisCpuPtr
	FnRedirectPeer               BpfBuiltinFunc = asm.FnRedirectPeer
	FnTaskStorageGet             BpfBuiltinFunc = asm.FnTaskStorageGet
	FnTaskStorageDelete          BpfBuiltinFunc = asm.FnTaskStorageDelete
	FnGetCurrentTaskBtf          BpfBuiltinFunc = asm.FnGetCurrentTaskBtf
	FnBprmOptsSet                BpfBuiltinFunc = asm.FnBprmOptsSet
	FnKtimeGetCoarseNs           BpfBuiltinFunc = asm.FnKtimeGetCoarseNs
	FnImaInodeHash               BpfBuiltinFunc = asm.FnImaInodeHash
	FnSockFromFile               BpfBuiltinFunc = asm.FnSockFromFile
	FnCheckMtu                   BpfBuiltinFunc = asm.FnCheckMtu
	FnForEachMapElem             BpfBuiltinFunc = asm.FnForEachMapElem
	FnSnprintf                   BpfBuiltinFunc = asm.FnSnprintf
	FnSysBpf                     BpfBuiltinFunc = asm.FnSysBpf
	FnBtfFindByNameKind          BpfBuiltinFunc = asm.FnBtfFindByNameKind
	FnSysClose                   BpfBuiltinFunc = asm.FnSysClose
	FnTimerInit                  BpfBuiltinFunc = asm.FnTimerInit
	FnTimerSetCallback           BpfBuiltinFunc = asm.FnTimerSetCallback
	FnTimerStart                 BpfBuiltinFunc = asm.FnTimerStart
	FnTimerCancel                BpfBuiltinFunc = asm.FnTimerCancel
	FnGetFuncIp                  BpfBuiltinFunc = asm.FnGetFuncIp
	FnGetAttachCookie            BpfBuiltinFunc = asm.FnGetAttachCookie
	FnTaskPtRegs                 BpfBuiltinFunc = asm.FnTaskPtRegs
	FnGetBranchSnapshot          BpfBuiltinFunc = asm.FnGetBranchSnapshot
	FnTraceVprintk               BpfBuiltinFunc = asm.FnTraceVprintk
	FnSkcToUnixSock              BpfBuiltinFunc = asm.FnSkcToUnixSock
	FnKallsymsLookupName         BpfBuiltinFunc = asm.FnKallsymsLookupName
	FnFindVma                    BpfBuiltinFunc = asm.FnFindVma
	FnLoop                       BpfBuiltinFunc = asm.FnLoop
	FnStrncmp                    BpfBuiltinFunc = asm.FnStrncmp
	FnGetFuncArg                 BpfBuiltinFunc = asm.FnGetFuncArg
	FnGetFuncRet                 BpfBuiltinFunc = asm.FnGetFuncRet
	FnGetFuncArgCnt              BpfBuiltinFunc = asm.FnGetFuncArgCnt
	FnGetRetval                  BpfBuiltinFunc = asm.FnGetRetval
	FnSetRetval                  BpfBuiltinFunc = asm.FnSetRetval
	FnXdpGetBuffLen              BpfBuiltinFunc = asm.FnXdpGetBuffLen
	FnXdpLoadBytes               BpfBuiltinFunc = asm.FnXdpLoadBytes
	FnXdpStoreBytes              BpfBuiltinFunc = asm.FnXdpStoreBytes
	FnCopyFromUserTask           BpfBuiltinFunc = asm.FnCopyFromUserTask
	FnSkbSetTstamp               BpfBuiltinFunc = asm.FnSkbSetTstamp
	FnImaFileHash                BpfBuiltinFunc = asm.FnImaFileHash
	FnKptrXchg                   BpfBuiltinFunc = asm.FnKptrXchg
	FnMapLookupPercpuElem        BpfBuiltinFunc = asm.FnMapLookupPercpuElem
	FnSkcToMptcpSock             BpfBuiltinFunc = asm.FnSkcToMptcpSock
	FnDynptrFromMem              BpfBuiltinFunc = asm.FnDynptrFromMem
	FnRingbufReserveDynptr       BpfBuiltinFunc = asm.FnRingbufReserveDynptr
	FnRingbufSubmitDynptr        BpfBuiltinFunc = asm.FnRingbufSubmitDynptr
	FnRingbufDiscardDynptr       BpfBuiltinFunc = asm.FnRingbufDiscardDynptr
	FnDynptrRead                 BpfBuiltinFunc = asm.FnDynptrRead
	FnDynptrWrite                BpfBuiltinFunc = asm.FnDynptrWrite
	FnDynptrData                 BpfBuiltinFunc = asm.FnDynptrData
	FnTcpRawGenSyncookieIpv4     BpfBuiltinFunc = asm.FnTcpRawGenSyncookieIpv4
	FnTcpRawGenSyncookieIpv6     BpfBuiltinFunc = asm.FnTcpRawGenSyncookieIpv6
	FnTcpRawCheckSyncookieIpv4   BpfBuiltinFunc = asm.FnTcpRawCheckSyncookieIpv4
	FnTcpRawCheckSyncookieIpv6   BpfBuiltinFunc = asm.FnTcpRawCheckSyncookieIpv6
	FnKtimeGetTaiNs              BpfBuiltinFunc = asm.FnKtimeGetTaiNs
	FnUserRingbufDrain           BpfBuiltinFunc = asm.FnUserRingbufDrain
	FnCgrpStorageGet             BpfBuiltinFunc = asm.FnCgrpStorageGet
	FnCgrpStorageDelete          BpfBuiltinFunc = asm.FnCgrpStorageDelete
)
