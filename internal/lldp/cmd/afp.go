// Copyright 2018 Google, Inc. All rights reserved.
//
// Use of this source code is governed by a BSD-style license
// that can be found in the LICENSE file in the root of the source
// tree.

// afpacket provides a simple example of using afpacket with zero-copy to read
// packet data.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"runtime/pprof"
	"syscall"
	"time"
	"unsafe"

	"github.com/google/gopacket"
	"github.com/google/gopacket/afpacket"
	"github.com/google/gopacket/layers"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"

	_ "github.com/google/gopacket/layers"
)

const ETH_P_LLDP = 0x88cc

var bpfFilter = []bpf.RawInstruction{
	{0x28, 0, 0, 0x0000000c},
	{0x15, 0, 5, 0x000088cc},
	{0x20, 0, 0, 0x00000002},
	{0x15, 0, 3, 0xc200000e},
	{0x28, 0, 0, 0x00000000},
	{0x15, 0, 1, 0x00000180},
	{0x6, 0, 0, 0x00040000},
	{0x6, 0, 0, 0x00000000},
}

func rawSocketaddrFromMAC(mac net.HardwareAddr) (sockaddr syscall.RawSockaddr) {
	for i, n := range mac {
		sockaddr.Data[i] = uint8(n)
	}
	return
}

var (
	iface      = flag.String("i", "any", "Interface to read from")
	cpuprofile = flag.String("cpuprofile", "", "If non-empty, write CPU profile here")
	snaplen    = flag.Int("s", 0, "Snaplen, if <= 0, use 65535")
	bufferSize = flag.Int("b", 8, "Interface buffersize (MB)")
	filter     = flag.String("f", "port not 22", "BPF filter")
	count      = flag.Int64("c", -1, "If >= 0, # of packets to capture before returning")
	verbose    = flag.Int64("log_every", 1, "Write a log every X packets")
	addVLAN    = flag.Bool("add_vlan", false, "If true, add VLAN header")
)

type afpacketHandle struct {
	TPacket *afpacket.TPacket
}

const IFNAMSIZ = 16

type ifreq struct {
	ifrName   [IFNAMSIZ]byte
	ifrHwaddr syscall.RawSockaddr
}

// addMulticastAddr adds a multicast address to an interface using an ioctl call
func addMulticastAddr(intf string, addr string) error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	var name [IFNAMSIZ]byte
	copy(name[:], []byte(intf))

	mac, _ := net.ParseMAC(addr)
	ifr := &ifreq{
		ifrName:   name,
		ifrHwaddr: rawSocketaddrFromMAC(mac),
	}

	_, _, ep := unix.Syscall(unix.SYS_IOCTL, uintptr(fd),
		unix.SIOCADDMULTI, uintptr(unsafe.Pointer(ifr)))

	if ep != 0 {
		return syscall.Errno(ep)
	}
	return nil
}

func newAfpacketHandle(device string, snaplen int, block_size int, num_blocks int,
	useVLAN bool, timeout time.Duration) (*afpacketHandle, error) {

	h := &afpacketHandle{}
	var err error

	if device == "any" {
		h.TPacket, err = afpacket.NewTPacket(
			afpacket.OptFrameSize(snaplen),
			afpacket.OptBlockSize(block_size),
			afpacket.OptNumBlocks(num_blocks),
			afpacket.OptAddVLANHeader(useVLAN),
			afpacket.OptPollTimeout(timeout),
			afpacket.SocketRaw,
			afpacket.TPacketVersion3)
	} else {
		h.TPacket, err = afpacket.NewTPacket(
			afpacket.OptInterface(device),
			afpacket.OptFrameSize(snaplen),
			afpacket.OptBlockSize(block_size),
			afpacket.OptNumBlocks(num_blocks),
			afpacket.OptAddVLANHeader(useVLAN),
			afpacket.OptPollTimeout(timeout),
			afpacket.SocketRaw,
			afpacket.TPacketVersion3)
	}
	return h, err
}

// ZeroCopyReadPacketData satisfies ZeroCopyPacketDataSource interface
func (h *afpacketHandle) ZeroCopyReadPacketData() (data []byte, ci gopacket.CaptureInfo, err error) {
	return h.TPacket.ZeroCopyReadPacketData()
}

// LinkType returns ethernet link type.
func (h *afpacketHandle) LinkType() layers.LinkType {
	return layers.LinkTypeEthernet
}

// Close will close afpacket source.
func (h *afpacketHandle) Close() {
	h.TPacket.Close()
}

// SocketStats prints received, dropped, queue-freeze packet stats.
func (h *afpacketHandle) SocketStats() (as afpacket.SocketStats, asv afpacket.SocketStatsV3, err error) {
	return h.TPacket.SocketStats()
}

// afpacketComputeSize computes the block_size and the num_blocks in such a way that the
// allocated mmap buffer is close to but smaller than target_size_mb.
// The restriction is that the block_size must be divisible by both the
// frame size and page size.
func afpacketComputeSize(targetSizeMb int, snaplen int, pageSize int) (
	frameSize int, blockSize int, numBlocks int, err error) {

	if snaplen < pageSize {
		frameSize = pageSize / (pageSize / snaplen)
	} else {
		frameSize = (snaplen/pageSize + 1) * pageSize
	}

	// 128 is the default from the gopacket library so just use that
	blockSize = frameSize * 128
	numBlocks = (targetSizeMb * 1024 * 1024) / blockSize

	if numBlocks == 0 {
		return 0, 0, 0, fmt.Errorf("Interface buffersize is too small")
	}

	return frameSize, blockSize, numBlocks, nil
}

func main() {
	flag.Parse()
	if *cpuprofile != "" {
		log.Printf("Writing CPU profile to %q", *cpuprofile)
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}
	log.Printf("Starting on interface %q", *iface)
	if *snaplen <= 0 {
		*snaplen = 65535
	}
	szFrame, szBlock, numBlocks, err := afpacketComputeSize(*bufferSize, *snaplen, os.Getpagesize())
	if err != nil {
		log.Fatal(err)
	}
	if *addVLAN {
		log.Printf("Adding VLAN header")
	}

	lldpPrefix := "01:80:c2:00:00"
	for _, lastByte := range []byte{0x00, 0x03, 0x0e} {
		// Add multicast address so that the kernel does not discard it
		if err := addMulticastAddr(*iface, fmt.Sprintf("%s:%02X", lldpPrefix, lastByte)); err != nil {
			log.Fatalf("Failed to add multicast address: %s", err)
		}
	}
	afpacketHandle, err := newAfpacketHandle(*iface, szFrame, szBlock, numBlocks, *addVLAN, -time.Millisecond*10)
	if err != nil {
		log.Fatal(err)
	}
	err = afpacketHandle.TPacket.SetBPF(bpfFilter)
	if err != nil {
		log.Fatal(err)
	}
	source := gopacket.NewPacketSource(afpacketHandle.TPacket, afpacketHandle.LinkType())
	defer afpacketHandle.Close()

	for packet := range source.Packets() {
		fmt.Println("packet", packet)
		lldpLayer := packet.Layer(layers.LayerTypeLinkLayerDiscovery)
		if lldpLayer != nil {
			fmt.Println("lldpLayer", lldpLayer)
		}
	}
}
