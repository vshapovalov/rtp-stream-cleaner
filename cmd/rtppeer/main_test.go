package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListSourcesNormalPCAP(t *testing.T) {
	pcapPath := filepath.Clean(filepath.Join("..", "..", "testdata", "normal.pcap"))

	var output bytes.Buffer
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = w

	err = listSources(pcapPath)

	_ = w.Close()
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("listSources: %v", err)
	}

	if _, err := output.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}

	got := make(map[uint32]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "ssrc=") {
			continue
		}
		var ssrc uint32
		if _, err := fmt.Sscanf(line, "ssrc=0x%08x", &ssrc); err == nil {
			got[ssrc] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan output: %v", err)
	}

	expected := []uint32{
		0x220a3aad,
		0x8d927c74,
		0x259989ef,
		0xedcc15a7,
	}
	for _, ssrc := range expected {
		if _, ok := got[ssrc]; !ok {
			t.Fatalf("missing ssrc 0x%08x in output: %s", ssrc, output.String())
		}
	}
}
