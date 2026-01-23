package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"rtp-stream-cleaner/internal/logging"
	"rtp-stream-cleaner/internal/pcapio"
	"rtp-stream-cleaner/internal/rtpfix"
	"rtp-stream-cleaner/internal/rtpparse"
)

type pacingMode int

const (
	pacingCapture pacingMode = iota
	pacingFast
	pacingFixed
)

type pacingConfig struct {
	mode    pacingMode
	fixed   time.Duration
	rawText string
}

type stats struct {
	sentAudioPkts int64
	sentVideoPkts int64
	recvAudioPkts int64
	recvVideoPkts int64
	sentBytes     int64
	recvBytes     int64
	parseErrors   int64
	sendErrors    int64
}

type config struct {
	bindIP      string
	audioPort   int
	videoPort   int
	audioTo     string
	videoTo     string
	audioSSRC   uint32
	videoSSRC   uint32
	sendPCAP    string
	recvPCAP    string
	pacing      pacingConfig
	duration    time.Duration
	verbose     bool
	listSources bool
}

func main() {
	cfg, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("rtppeer", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&cfg.bindIP, "bind-ip", "127.0.0.1", "Bind IP address")
	flags.IntVar(&cfg.audioPort, "audio-port", 0, "Local audio UDP port")
	flags.IntVar(&cfg.videoPort, "video-port", 0, "Local video UDP port")
	flags.StringVar(&cfg.audioTo, "audio-to", "", "Audio destination ip:port")
	flags.StringVar(&cfg.videoTo, "video-to", "", "Video destination ip:port")
	flags.StringVar(&cfg.sendPCAP, "send-pcap", "", "PCAP file to replay")
	flags.StringVar(&cfg.recvPCAP, "recv-pcap", "", "PCAP file to write")
	flags.BoolVar(&cfg.listSources, "list-sources", false, "List RTP SSRCs and payload types in send-pcap and exit")
	pacingRaw := flags.String("pacing", "capture", "Pacing mode: capture, fast, fixed:<ms>")
	audioSSRC := flags.String("audio-ssrc", "", "Audio RTP SSRC (hex or decimal)")
	videoSSRC := flags.String("video-ssrc", "", "Video RTP SSRC (hex or decimal)")
	var durationSec int
	flags.IntVar(&durationSec, "duration", 0, "Duration in seconds to run")
	flags.BoolVar(&cfg.verbose, "verbose", false, "Verbose logging")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.listSources {
		if cfg.sendPCAP == "" {
			return cfg, errors.New("send-pcap is required when list-sources is set")
		}
	} else {
		if cfg.audioPort == 0 || cfg.videoPort == 0 {
			return cfg, errors.New("audio-port and video-port are required")
		}
		if cfg.sendPCAP != "" {
			if cfg.audioTo == "" || cfg.videoTo == "" {
				return cfg, errors.New("audio-to and video-to are required when send-pcap is set")
			}
			if *audioSSRC == "" || *videoSSRC == "" {
				return cfg, errors.New("audio-ssrc and video-ssrc are required when send-pcap is set")
			}
			var err error
			cfg.audioSSRC, err = parseSSRC(*audioSSRC)
			if err != nil {
				return cfg, fmt.Errorf("invalid audio-ssrc: %w", err)
			}
			cfg.videoSSRC, err = parseSSRC(*videoSSRC)
			if err != nil {
				return cfg, fmt.Errorf("invalid video-ssrc: %w", err)
			}
		}
	}
	cfg.duration = time.Duration(durationSec) * time.Second
	pacingCfg, err := parsePacing(*pacingRaw)
	if err != nil {
		return cfg, err
	}
	cfg.pacing = pacingCfg
	return cfg, nil
}

func parseSSRC(value string) (uint32, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, errors.New("empty ssrc")
	}
	base := 10
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		base = 0
	} else if strings.IndexFunc(trimmed, func(r rune) bool { return (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') }) != -1 {
		base = 16
	}
	parsed, err := strconv.ParseUint(trimmed, base, 32)
	if err != nil {
		return 0, err
	}
	return uint32(parsed), nil
}

func parsePacing(value string) (pacingConfig, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "capture" {
		return pacingConfig{mode: pacingCapture, rawText: value}, nil
	}
	if trimmed == "fast" {
		return pacingConfig{mode: pacingFast, rawText: value}, nil
	}
	if strings.HasPrefix(trimmed, "fixed:") {
		msStr := strings.TrimPrefix(trimmed, "fixed:")
		ms, err := strconv.Atoi(msStr)
		if err != nil || ms < 0 {
			return pacingConfig{}, fmt.Errorf("invalid fixed pacing: %s", value)
		}
		return pacingConfig{mode: pacingFixed, fixed: time.Duration(ms) * time.Millisecond, rawText: value}, nil
	}
	return pacingConfig{}, fmt.Errorf("unknown pacing mode: %s", value)
}

func run(cfg config) error {
	if cfg.listSources {
		return listSources(cfg.sendPCAP)
	}
	logger := logging.L()
	bindIP := net.ParseIP(cfg.bindIP)
	if bindIP == nil {
		return fmt.Errorf("invalid bind-ip: %s", cfg.bindIP)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.duration)
		defer cancel()
	}

	audioConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: cfg.audioPort})
	if err != nil {
		return fmt.Errorf("listen audio: %w", err)
	}
	defer audioConn.Close()
	videoConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: bindIP, Port: cfg.videoPort})
	if err != nil {
		return fmt.Errorf("listen video: %w", err)
	}
	defer videoConn.Close()

	var recvWriter *pcapio.Writer
	if cfg.recvPCAP != "" {
		writer, err := pcapio.NewWriter(cfg.recvPCAP)
		if err != nil {
			return err
		}
		recvWriter = writer
		defer func() {
			if err := recvWriter.Close(); err != nil {
				logger.Error("close pcap writer", "error", err)
			}
		}()
	}

	if cfg.verbose {
		logger.Info("audio socket bound", "addr", audioConn.LocalAddr())
		logger.Info("video socket bound", "addr", videoConn.LocalAddr())
	}

	var stats stats
	var wg sync.WaitGroup

	if cfg.recvPCAP != "" || cfg.sendPCAP == "" {
		wg.Add(2)
		go recvLoop(ctx, "audio", audioConn, recvWriter, cfg.verbose, logger, &stats, &wg)
		go recvLoop(ctx, "video", videoConn, recvWriter, cfg.verbose, logger, &stats, &wg)
	}

	sendDone := make(chan error, 1)
	if cfg.sendPCAP != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sendDone <- sendLoop(ctx, cfg, audioConn, videoConn, logger, &stats)
		}()
	}

	if cfg.sendPCAP != "" && cfg.duration == 0 {
		select {
		case err := <-sendDone:
			if err != nil {
				return err
			}
			stop()
		case <-ctx.Done():
		}
	}

	<-ctx.Done()
	wg.Wait()

	printSummary(&stats)
	return nil
}

func recvLoop(ctx context.Context, label string, conn *net.UDPConn, writer *pcapio.Writer, verbose bool, logger *slog.Logger, stats *stats, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, 64*1024)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return
				default:
					continue
				}
			}
			if ctx.Err() != nil {
				return
			}
			logger.Error("recv failed", "label", label, "error", err)
			continue
		}
		atomic.AddInt64(&stats.recvBytes, int64(n))
		if label == "audio" {
			atomic.AddInt64(&stats.recvAudioPkts, 1)
		} else {
			atomic.AddInt64(&stats.recvVideoPkts, 1)
		}
		payload := make([]byte, n)
		copy(payload, buf[:n])
		if verbose {
			logger.Info("recv packet", "label", label, "bytes", n, "addr", addr.String())
		}
		if writer != nil {
			localAddr := conn.LocalAddr().(*net.UDPAddr)
			if err := writer.WritePacket(time.Now(), addr.IP, localAddr.IP, addr.Port, localAddr.Port, payload); err != nil {
				logger.Error("pcap write error", "error", err)
			}
		}
	}
}

func sendLoop(ctx context.Context, cfg config, audioConn, videoConn *net.UDPConn, logger *slog.Logger, stats *stats) error {
	audioAddr, err := net.ResolveUDPAddr("udp", cfg.audioTo)
	if err != nil {
		return fmt.Errorf("resolve audio-to: %w", err)
	}
	videoAddr, err := net.ResolveUDPAddr("udp", cfg.videoTo)
	if err != nil {
		return fmt.Errorf("resolve video-to: %w", err)
	}
	reader, err := pcapio.OpenReader(cfg.sendPCAP)
	if err != nil {
		return err
	}
	defer reader.Close()

	var prevTS time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		packet, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		udpPayload, err := extractUDPPayload(packet.Data, reader.LinkType())
		if err != nil {
			atomic.AddInt64(&stats.parseErrors, 1)
			continue
		}
		if len(udpPayload) == 0 {
			continue
		}
		rtpPacket, err := rtpparse.Parse(udpPayload)
		if err != nil {
			atomic.AddInt64(&stats.parseErrors, 1)
			continue
		}
		var conn *net.UDPConn
		var addr *net.UDPAddr
		var label string
		if rtpPacket.SSRC == cfg.audioSSRC {
			conn = audioConn
			addr = audioAddr
			label = "audio"
		} else if rtpPacket.SSRC == cfg.videoSSRC {
			conn = videoConn
			addr = videoAddr
			label = "video"
		} else {
			continue
		}
		if err := applyPacing(cfg.pacing, packet.Timestamp, &prevTS); err != nil {
			return err
		}
		if _, err := conn.WriteToUDP(udpPayload, addr); err != nil {
			atomic.AddInt64(&stats.sendErrors, 1)
			if cfg.verbose {
				logger.Error("send failed", "label", label, "error", err)
			}
			continue
		}
		atomic.AddInt64(&stats.sentBytes, int64(len(udpPayload)))
		if label == "audio" {
			atomic.AddInt64(&stats.sentAudioPkts, 1)
		} else {
			atomic.AddInt64(&stats.sentVideoPkts, 1)
		}
		if cfg.verbose {
			logger.Info("sent packet", "label", label, "bytes", len(udpPayload), "addr", addr.String())
		}
	}
	return nil
}

func applyPacing(cfg pacingConfig, ts time.Time, prevTS *time.Time) error {
	switch cfg.mode {
	case pacingFast:
		return nil
	case pacingFixed:
		if cfg.fixed > 0 {
			time.Sleep(cfg.fixed)
		}
		return nil
	case pacingCapture:
		if prevTS.IsZero() {
			*prevTS = ts
			return nil
		}
		delta := ts.Sub(*prevTS)
		if delta > 0 {
			time.Sleep(delta)
		}
		*prevTS = ts
		return nil
	default:
		return fmt.Errorf("unknown pacing mode: %s", cfg.rawText)
	}
}

func printSummary(stats *stats) {
	fmt.Println("rtppeer summary")
	fmt.Printf("sent_audio_pkts=%d\n", atomic.LoadInt64(&stats.sentAudioPkts))
	fmt.Printf("sent_video_pkts=%d\n", atomic.LoadInt64(&stats.sentVideoPkts))
	fmt.Printf("recv_audio_pkts=%d\n", atomic.LoadInt64(&stats.recvAudioPkts))
	fmt.Printf("recv_video_pkts=%d\n", atomic.LoadInt64(&stats.recvVideoPkts))
	fmt.Printf("bytes_sent=%d\n", atomic.LoadInt64(&stats.sentBytes))
	fmt.Printf("bytes_recv=%d\n", atomic.LoadInt64(&stats.recvBytes))
	fmt.Printf("errors=%d\n", atomic.LoadInt64(&stats.parseErrors)+atomic.LoadInt64(&stats.sendErrors))
}

func listSources(pcapPath string) error {
	reader, err := pcapio.OpenReader(pcapPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	type sourceStats struct {
		packets int
		sps     int
		pps     int
		idr     int
		nonIDR  int
	}
	sources := make(map[uint32]map[uint8]*sourceStats)
	for {
		packet, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		udpPayload, err := extractUDPPayload(packet.Data, reader.LinkType())
		if err != nil || len(udpPayload) == 0 {
			continue
		}
		rtpPacket, err := rtpparse.Parse(udpPayload)
		if err != nil {
			continue
		}
		payloadTypes, ok := sources[rtpPacket.SSRC]
		if !ok {
			payloadTypes = make(map[uint8]*sourceStats)
			sources[rtpPacket.SSRC] = payloadTypes
		}
		stats, ok := payloadTypes[rtpPacket.PayloadType]
		if !ok {
			stats = &sourceStats{}
			payloadTypes[rtpPacket.PayloadType] = stats
		}
		stats.packets++
		if rtpPacket.HeaderSize < len(udpPayload) {
			rtpPayload := udpPayload[rtpPacket.HeaderSize:]
			if info, ok := rtpfix.ParseH264(rtpPayload); ok {
				if info.IsFU && !info.FUStart {
					continue
				}
				if info.IsSPS {
					stats.sps++
				}
				if info.IsPPS {
					stats.pps++
				}
				if info.IsSlice {
					if info.IsIDR {
						stats.idr++
					} else {
						stats.nonIDR++
					}
				}
			}
		}
	}

	ssrcs := make([]uint32, 0, len(sources))
	for ssrc := range sources {
		ssrcs = append(ssrcs, ssrc)
	}
	sort.Slice(ssrcs, func(i, j int) bool { return ssrcs[i] < ssrcs[j] })
	for _, ssrc := range ssrcs {
		payloadTypes := sources[ssrc]
		payloadList := make([]int, 0, len(payloadTypes))
		for pt := range payloadTypes {
			payloadList = append(payloadList, int(pt))
		}
		sort.Ints(payloadList)
		for _, pt := range payloadList {
			stats := payloadTypes[uint8(pt)]
			fmt.Printf(
				"ssrc=0x%08x payload_type=%d packets=%d sps=%d pps=%d idr=%d non_idr=%d\n",
				ssrc,
				pt,
				stats.packets,
				stats.sps,
				stats.pps,
				stats.idr,
				stats.nonIDR,
			)
		}
	}
	return nil
}

func extractUDPPayload(frame []byte, linkType uint32) ([]byte, error) {
	var etherType uint16
	offset := 0
	switch linkType {
	case 1:
		if len(frame) < 14 {
			return nil, fmt.Errorf("frame too short")
		}
		etherType = binary.BigEndian.Uint16(frame[12:14])
		offset = 14
	case 113:
		if len(frame) < 16 {
			return nil, fmt.Errorf("frame too short")
		}
		etherType = binary.BigEndian.Uint16(frame[14:16])
		offset = 16
	case 276:
		if len(frame) < 20 {
			return nil, fmt.Errorf("frame too short")
		}
		etherType = binary.BigEndian.Uint16(frame[0:2])
		offset = 20
	default:
		return nil, fmt.Errorf("unsupported linktype: %d", linkType)
	}
	if etherType == 0x8100 {
		if len(frame) < offset+4 {
			return nil, fmt.Errorf("frame too short for vlan")
		}
		etherType = binary.BigEndian.Uint16(frame[offset+2 : offset+4])
		offset += 4
	}
	if etherType != 0x0800 {
		return nil, fmt.Errorf("unsupported ethertype: 0x%x", etherType)
	}
	if len(frame) < offset+20 {
		return nil, fmt.Errorf("ipv4 header truncated")
	}
	ihl := int(frame[offset] & 0x0f)
	if ihl < 5 {
		return nil, fmt.Errorf("invalid ihl")
	}
	ipHeaderLen := ihl * 4
	if len(frame) < offset+ipHeaderLen {
		return nil, fmt.Errorf("ipv4 header truncated")
	}
	if frame[offset+9] != 17 {
		return nil, fmt.Errorf("not udp")
	}
	frag := binary.BigEndian.Uint16(frame[offset+6 : offset+8])
	if frag&0x1fff != 0 {
		return nil, fmt.Errorf("fragmented packet")
	}
	udpStart := offset + ipHeaderLen
	if len(frame) < udpStart+8 {
		return nil, fmt.Errorf("udp header truncated")
	}
	udpLen := int(binary.BigEndian.Uint16(frame[udpStart+4 : udpStart+6]))
	if udpLen < 8 {
		return nil, fmt.Errorf("invalid udp length")
	}
	payloadLen := udpLen - 8
	if len(frame) < udpStart+8+payloadLen {
		return nil, fmt.Errorf("udp payload truncated")
	}
	payload := make([]byte, payloadLen)
	copy(payload, frame[udpStart+8:udpStart+8+payloadLen])
	return payload, nil
}
