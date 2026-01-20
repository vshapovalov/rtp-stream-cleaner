package pcapio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

const (
	pcapMagicLittle = 0xa1b2c3d4
	pcapMagicBig    = 0xd4c3b2a1
	pcapNgMagic     = 0x0a0d0d0a
	linkTypeEther   = 1
	defaultSnap     = 65535
)

type byteOrder binary.ByteOrder

// Packet represents a captured packet.
type Packet struct {
	Timestamp time.Time
	Data      []byte
}

// Reader reads packets from pcap or pcapng files.
type Reader struct {
	file       *os.File
	linkType   uint32
	byteOrder  binary.ByteOrder
	isPcapng   bool
	ngIfaces   map[uint32]ngInterface
	ngSection  *ngSection
	ngFinished bool
}

type ngInterface struct {
	linkType uint16
	tsRes    time.Duration
}

type ngSection struct {
	byteOrder binary.ByteOrder
}

// OpenReader opens a pcap or pcapng reader.
func OpenReader(path string) (*Reader, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pcap: %w", err)
	}
	var magicBuf [4]byte
	if _, err := io.ReadFull(file, magicBuf[:]); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("read pcap magic: %w", err)
	}
	magic := binary.BigEndian.Uint32(magicBuf[:])
	switch magic {
	case pcapNgMagic:
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("seek pcapng: %w", err)
		}
		return &Reader{file: file, isPcapng: true, ngIfaces: make(map[uint32]ngInterface)}, nil
	case pcapMagicLittle, pcapMagicBig:
		var bo binary.ByteOrder = binary.LittleEndian
		if magic == pcapMagicBig {
			bo = binary.BigEndian
		}
		reader := &Reader{file: file, byteOrder: bo}
		if err := reader.readPcapHeader(); err != nil {
			_ = file.Close()
			return nil, err
		}
		return reader, nil
	default:
		_ = file.Close()
		return nil, fmt.Errorf("unsupported pcap magic: 0x%x", magic)
	}
}

// Close closes the reader file.
func (r *Reader) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

// LinkType returns link type (pcap) or last seen link type (pcapng).
func (r *Reader) LinkType() uint32 {
	return r.linkType
}

// Next returns the next packet.
func (r *Reader) Next() (Packet, error) {
	if r.isPcapng {
		return r.nextPcapng()
	}
	return r.nextPcap()
}

func (r *Reader) readPcapHeader() error {
	header := make([]byte, 20)
	if _, err := io.ReadFull(r.file, header); err != nil {
		return fmt.Errorf("read pcap header: %w", err)
	}
	r.linkType = r.byteOrder.Uint32(header[16:20])
	return nil
}

func (r *Reader) nextPcap() (Packet, error) {
	var hdr [16]byte
	if _, err := io.ReadFull(r.file, hdr[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Packet{}, io.EOF
		}
		return Packet{}, fmt.Errorf("read pcap record header: %w", err)
	}
	tsSec := r.byteOrder.Uint32(hdr[0:4])
	tsUsec := r.byteOrder.Uint32(hdr[4:8])
	inclLen := r.byteOrder.Uint32(hdr[8:12])
	data := make([]byte, inclLen)
	if _, err := io.ReadFull(r.file, data); err != nil {
		return Packet{}, fmt.Errorf("read pcap record data: %w", err)
	}
	ts := time.Unix(int64(tsSec), int64(tsUsec)*1000)
	return Packet{Timestamp: ts, Data: data}, nil
}

func (r *Reader) nextPcapng() (Packet, error) {
	for {
		var blockHdr [8]byte
		if _, err := io.ReadFull(r.file, blockHdr[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return Packet{}, io.EOF
			}
			return Packet{}, fmt.Errorf("read pcapng block header: %w", err)
		}
		blockType := binary.LittleEndian.Uint32(blockHdr[0:4])
		totalLen := binary.LittleEndian.Uint32(blockHdr[4:8])
		if totalLen < 12 {
			return Packet{}, fmt.Errorf("invalid pcapng block length")
		}
		payloadLen := int(totalLen) - 12
		payload := make([]byte, payloadLen)
		if _, err := io.ReadFull(r.file, payload); err != nil {
			return Packet{}, fmt.Errorf("read pcapng block payload: %w", err)
		}
		var trailer [4]byte
		if _, err := io.ReadFull(r.file, trailer[:]); err != nil {
			return Packet{}, fmt.Errorf("read pcapng block trailer: %w", err)
		}
		switch blockType {
		case 0x0A0D0D0A:
			if len(payload) < 4 {
				return Packet{}, fmt.Errorf("pcapng section header too short")
			}
			byteOrderMagic := binary.LittleEndian.Uint32(payload[0:4])
			if byteOrderMagic == 0x1A2B3C4D {
				r.ngSection = &ngSection{byteOrder: binary.LittleEndian}
			} else if byteOrderMagic == 0x4D3C2B1A {
				r.ngSection = &ngSection{byteOrder: binary.BigEndian}
			} else {
				return Packet{}, fmt.Errorf("unknown pcapng byte order magic")
			}
		case 0x00000001:
			if len(payload) < 8 {
				return Packet{}, fmt.Errorf("pcapng interface header too short")
			}
			var bo binary.ByteOrder = binary.LittleEndian
			if r.ngSection != nil {
				bo = r.ngSection.byteOrder
			}
			linkType := bo.Uint16(payload[0:2])
			ifaceID := uint32(len(r.ngIfaces))
			iface := ngInterface{linkType: linkType, tsRes: time.Microsecond}
			parseNgOptions(payload[8:], func(code uint16, value []byte) {
				if code == 9 && len(value) >= 1 {
					res := value[0]
					if res&0x80 == 0 {
						iface.tsRes = time.Second / time.Duration(1<<res)
					} else {
						iface.tsRes = time.Second / time.Duration(10<<uint(res&0x7f))
					}
				}
			})
			r.ngIfaces[ifaceID] = iface
			if r.linkType == 0 {
				r.linkType = uint32(linkType)
			}
		case 0x00000006:
			if len(payload) < 20 {
				return Packet{}, fmt.Errorf("pcapng packet header too short")
			}
			var bo binary.ByteOrder = binary.LittleEndian
			if r.ngSection != nil {
				bo = r.ngSection.byteOrder
			}
			ifaceID := bo.Uint32(payload[0:4])
			iface, ok := r.ngIfaces[ifaceID]
			if !ok {
				iface = ngInterface{linkType: linkTypeEther, tsRes: time.Microsecond}
			}
			r.linkType = uint32(iface.linkType)
			tsHigh := bo.Uint32(payload[4:8])
			tsLow := bo.Uint32(payload[8:12])
			capLen := bo.Uint32(payload[12:16])
			if int(20+capLen) > len(payload) {
				return Packet{}, fmt.Errorf("pcapng packet data too short")
			}
			data := make([]byte, capLen)
			copy(data, payload[20:20+capLen])
			timestamp := (uint64(tsHigh) << 32) | uint64(tsLow)
			ts := time.Unix(0, int64(timestamp)*int64(iface.tsRes))
			return Packet{Timestamp: ts, Data: data}, nil
		default:
			// Skip other block types.
		}
	}
}

func parseNgOptions(data []byte, fn func(code uint16, value []byte)) {
	for len(data) >= 4 {
		code := binary.LittleEndian.Uint16(data[0:2])
		length := binary.LittleEndian.Uint16(data[2:4])
		data = data[4:]
		if code == 0 {
			return
		}
		if int(length) > len(data) {
			return
		}
		value := data[:length]
		fn(code, value)
		pad := (4 - (int(length) % 4)) % 4
		if int(length)+pad > len(data) {
			return
		}
		data = data[int(length)+pad:]
	}
}

// Writer writes packets into a pcap file with synthetic Ethernet/IPv4/UDP headers.
type Writer struct {
	file   *os.File
	mu     sync.Mutex
	closed bool
}

// NewWriter creates a pcap writer.
func NewWriter(path string) (*Writer, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create pcap: %w", err)
	}
	writer := &Writer{file: file}
	if err := writer.writeHeader(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return writer, nil
}

func (w *Writer) writeHeader() error {
	header := make([]byte, 24)
	binary.LittleEndian.PutUint32(header[0:4], pcapMagicLittle)
	binary.LittleEndian.PutUint16(header[4:6], 2)
	binary.LittleEndian.PutUint16(header[6:8], 4)
	binary.LittleEndian.PutUint32(header[8:12], 0)
	binary.LittleEndian.PutUint32(header[12:16], 0)
	binary.LittleEndian.PutUint32(header[16:20], defaultSnap)
	binary.LittleEndian.PutUint32(header[20:24], linkTypeEther)
	_, err := w.file.Write(header)
	if err != nil {
		return fmt.Errorf("write pcap header: %w", err)
	}
	return nil
}

// Close closes the underlying file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.file.Close()
}

// WritePacket writes a single UDP packet to the pcap.
func (w *Writer) WritePacket(ts time.Time, srcIP, dstIP net.IP, srcPort, dstPort int, payload []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("pcap writer closed")
	}
	frame, err := buildEthernetIPv4UDP(srcIP, dstIP, srcPort, dstPort, payload)
	if err != nil {
		return err
	}
	hdr := make([]byte, 16)
	secs := uint32(ts.Unix())
	usecs := uint32(ts.Nanosecond() / 1000)
	binary.LittleEndian.PutUint32(hdr[0:4], secs)
	binary.LittleEndian.PutUint32(hdr[4:8], usecs)
	binary.LittleEndian.PutUint32(hdr[8:12], uint32(len(frame)))
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(frame)))
	if _, err := w.file.Write(hdr); err != nil {
		return fmt.Errorf("write pcap record header: %w", err)
	}
	if _, err := w.file.Write(frame); err != nil {
		return fmt.Errorf("write pcap record data: %w", err)
	}
	return nil
}

func buildEthernetIPv4UDP(srcIP, dstIP net.IP, srcPort, dstPort int, payload []byte) ([]byte, error) {
	src4 := srcIP.To4()
	dst4 := dstIP.To4()
	if src4 == nil {
		src4 = net.IPv4(192, 0, 2, 1)
	}
	if dst4 == nil {
		dst4 = net.IPv4(192, 0, 2, 2)
	}
	eth := make([]byte, 14)
	copy(eth[0:6], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x02})
	copy(eth[6:12], []byte{0x02, 0x00, 0x00, 0x00, 0x00, 0x01})
	binary.BigEndian.PutUint16(eth[12:14], 0x0800)

	ip := make([]byte, 20)
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+8+len(payload)))
	ip[8] = 64
	ip[9] = 17
	copy(ip[12:16], src4)
	copy(ip[16:20], dst4)
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip))

	udp := make([]byte, 8)
	binary.BigEndian.PutUint16(udp[0:2], uint16(srcPort))
	binary.BigEndian.PutUint16(udp[2:4], uint16(dstPort))
	binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(payload)))
	binary.BigEndian.PutUint16(udp[6:8], udpChecksum(ip, udp, payload))

	frame := make([]byte, 0, len(eth)+len(ip)+len(udp)+len(payload))
	frame = append(frame, eth...)
	frame = append(frame, ip...)
	frame = append(frame, udp...)
	frame = append(frame, payload...)
	return frame, nil
}

func checksum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
	}
	if len(data)%2 == 1 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func udpChecksum(ipHeader []byte, udpHeader []byte, payload []byte) uint16 {
	pseudo := make([]byte, 12)
	copy(pseudo[0:4], ipHeader[12:16])
	copy(pseudo[4:8], ipHeader[16:20])
	pseudo[8] = 0
	pseudo[9] = 17
	binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(udpHeader)+len(payload)))

	sum := uint32(0)
	sum += uint32(checksum(pseudo))
	udpCopy := make([]byte, len(udpHeader))
	copy(udpCopy, udpHeader)
	udpCopy[6] = 0
	udpCopy[7] = 0
	sum += uint32(checksum(udpCopy))
	sum += uint32(checksum(payload))
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	cs := ^uint16(sum)
	if cs == 0 {
		return 0xffff
	}
	return cs
}
