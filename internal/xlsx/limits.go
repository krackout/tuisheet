package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
)

// These limits keep opening an untrusted workbook within a predictable memory
// and CPU budget. They are application limits, not OpenXML format limits:
// ISO/IEC 29500 imposes no resource caps of its own (the only format bounds
// it defines are the grid itself, A1:XFD1048576, enforced in sheet.MaxRows/
// MaxColumns).
//
// History note: the original 64MB part / 1M-cell caps rejected legitimate
// real-world workbooks — a 553k-cell sheet ships as 31MB of XML and a
// 2M-cell sheet as ~70MB — so they were raised to admit ordinary large
// exports while still blocking decompression bombs.
const (
	maxArchiveEntries   = 4_096
	maxPartBytes        = 256 << 20
	maxArchiveBytes     = 512 << 20
	maxCompressionRatio = 2_000
	maxXMLDepth         = 128
	// Large legitimate workbooks easily reach tens of millions of tokens
	// (a 500k-cell sheet is ~3M); the cap exists to stop unbounded
	// decompression bombs, not to constrain real files.
	maxXMLTokens = 256_000_000

	maxWorkbookSheets = 256
	// Measured: ~350 bytes of retained heap per loaded cell, so 4M cells
	// peaks around 1.5GB — generous headroom over real exports while
	// staying inside a modest machine's budget.
	maxCellsPerSheet = 4_000_000
	maxMergeCells    = 100_000
	maxSharedStrings = 1_000_000
)

func validateZipFiles(files []*zip.File) error {
	if len(files) > maxArchiveEntries {
		return fmt.Errorf("archive contains %d entries; maximum is %d", len(files), maxArchiveEntries)
	}

	var total uint64
	for _, f := range files {
		if f.UncompressedSize64 > maxPartBytes {
			return fmt.Errorf("archive entry %q exceeds the %d-byte limit", f.Name, maxPartBytes)
		}
		if f.UncompressedSize64 > 0 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > maxCompressionRatio) {
			return fmt.Errorf("archive entry %q exceeds the %d:1 compression-ratio limit", f.Name, maxCompressionRatio)
		}
		if total > maxArchiveBytes-f.UncompressedSize64 {
			return fmt.Errorf("archive exceeds the %d-byte uncompressed limit", maxArchiveBytes)
		}
		total += f.UncompressedSize64
	}
	return nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxPartBytes {
		return nil, fmt.Errorf("entry exceeds the %d-byte limit", maxPartBytes)
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()

	data, err := io.ReadAll(io.LimitReader(r, maxPartBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPartBytes {
		return nil, fmt.Errorf("entry exceeds the %d-byte limit", maxPartBytes)
	}
	return data, nil
}

func validateXML(data []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for tokens := 0; ; tokens++ {
		if tokens >= maxXMLTokens {
			return fmt.Errorf("XML exceeds the %d-token limit", maxXMLTokens)
		}
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch token.(type) {
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return fmt.Errorf("XML exceeds the %d-element nesting limit", maxXMLDepth)
			}
		case xml.EndElement:
			depth--
		}
	}
}
