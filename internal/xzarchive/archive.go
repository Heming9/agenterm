// Package xzarchive provides deterministic, bounded tar.xz component archives.
package xzarchive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"sort"
	"time"

	"github.com/ulikunitz/xz"
	"github.com/ulikunitz/xz/lzma"
)

const MaxFileBytes = 64 << 20
const MaxTotalBytes = 512 << 20

// Write compresses all entries in one XZ stream, sharing a dictionary across files.
func Write(w io.Writer, files map[string][]byte) error {
	names := make([]string, 0, len(files))
	total := 0
	for name, data := range files {
		names = append(names, name)
		total += len(data)
	}
	sort.Strings(names)
	dictionary := 1 << 16
	for dictionary < total && dictionary < 64<<20 {
		dictionary *= 2
	}
	zw, err := (xz.WriterConfig{DictCap: dictionary, Matcher: lzma.HashTable4}).NewWriter(w)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(zw)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0755, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}

func Single(name string, data []byte) ([]byte, error) {
	var out bytes.Buffer
	err := Write(&out, map[string][]byte{name: data})
	return out.Bytes(), err
}

// Read accepts regular files only. The caller validates the allowed entry names.
// Consuming the complete XZ stream also verifies its checksum and rejects trailers.
func Read(data []byte) (map[string][]byte, error) {
	zr, err := (xz.ReaderConfig{DictCap: 64 << 20, SingleStream: true}).NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	files := map[string][]byte{}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg || h.Linkname != "" || h.Size <= 0 || h.Size > MaxFileBytes || len(files) >= 33 {
			return nil, errors.New("invalid component tar entry")
		}
		if _, exists := files[h.Name]; exists {
			return nil, errors.New("duplicate component tar path")
		}
		total += h.Size
		if total > MaxTotalBytes {
			return nil, errors.New("component archive exceeds size limit")
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		files[h.Name] = b
	}
	// tar can stop at its end marker before XZ has verified its checksum. Only
	// a bounded amount of zero tar padding is permitted after that marker.
	tail, err := io.ReadAll(io.LimitReader(zr, (64<<10)+1))
	if err != nil {
		return nil, err
	}
	if len(tail) > 64<<10 || len(bytes.Trim(tail, "\x00")) != 0 {
		return nil, errors.New("trailing component archive data")
	}
	return files, nil
}

// SingleFormat provides compatibility formats without changing the source binary.
func SingleFormat(name string, data []byte, format string) ([]byte, error) {
	if format == "tar.xz" {
		return Single(name, data)
	}
	var out bytes.Buffer
	if format == "zip" {
		zw := zip.NewWriter(&out)
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) { return flate.NewWriter(w, flate.BestCompression) })
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
		h.SetMode(0755)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err = w.Write(data); err != nil {
			return nil, err
		}
		if err = zw.Close(); err != nil {
			return nil, err
		}
	} else if format == "tar.gz" {
		gz, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
		if err != nil {
			return nil, err
		}
		tw := tar.NewWriter(gz)
		if err = tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0755, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			return nil, err
		}
		if _, err = tw.Write(data); err != nil {
			return nil, err
		}
		if err = tw.Close(); err != nil {
			return nil, err
		}
		if err = gz.Close(); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("unsupported component compression format")
	}
	return out.Bytes(), nil
}
