package tar_diff

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
)

const (
	DeltaOpData    = iota
	DeltaOpOpen    = iota
	DeltaOpCopy    = iota
	DeltaOpAddData = iota
	DeltaOpSeek    = iota
)

const (
	deltaDataChunkSize = 4 * 1024 * 1024
)

var deltaHeader = [...]byte{'t', 'a', 'r', 'd', 'f', '1', '\n', 0}

type deltaWriter struct {
	writer      *zstd.Encoder
	buffer      []byte
	currentFile string
	currentPos  uint64
}

func newDeltaWriter(writer io.Writer, compressionLevel int) (*deltaWriter, error) {
	_, err := writer.Write(deltaHeader[:])
	if err != nil {
		return nil, err
	}

	encoder, err := zstd.NewWriter(writer, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(compressionLevel)))
	if err != nil {
		return nil, err
	}
	d := deltaWriter{writer: encoder, buffer: make([]byte, 0, deltaDataChunkSize)}
	return &d, nil
}

func (d *deltaWriter) writeOp(op uint8, size uint64, data []byte) error {
	buf := make([]byte, 1+binary.MaxVarintLen64)
	buf[0] = op
	sizeLen := binary.PutUvarint(buf[1:], size)
	bufLen := 1 + sizeLen

	if _, err := d.writer.Write(buf[:bufLen]); err != nil {
		return err
	}

	if data != nil {
		if _, err := d.writer.Write(data); err != nil {
			return err
		}
	}

	return nil
}

func (d *deltaWriter) FlushBuffer() error {
	if len(d.buffer) == 0 {
		return nil
	}
	err := d.writeOp(DeltaOpData, uint64(len(d.buffer)), d.buffer)
	d.buffer = d.buffer[:0]
	return err
}

func (d *deltaWriter) Close() error {
	if d.writer == nil {
		return nil
	}
	err := d.writer.Close()
	d.writer = nil
	return err
}

func (d *deltaWriter) WriteContent(data []byte) error {
	d.buffer = append(d.buffer, data...)

	if len(d.buffer) >= deltaDataChunkSize {
		return d.FlushBuffer()
	} else {
		return nil
	}
}

// Switches to new file if needed and ensures we're at the start of it
func (d *deltaWriter) SetCurrentFile(filename string) error {
	if d.currentFile != filename {
		nameBytes := []byte(filename)
		err := d.FlushBuffer()
		if err != nil {
			return err
		}
		err = d.writeOp(DeltaOpOpen, uint64(len(nameBytes)), nameBytes)
		if err != nil {
			return err
		}

		d.currentFile = filename
		d.currentPos = 0
	}
	return nil
}

func (d *deltaWriter) Seek(pos uint64) error {
	if d.currentPos == pos {
		return nil
	}

	err := d.FlushBuffer()
	if err != nil {
		return err
	}

	err = d.writeOp(DeltaOpSeek, pos, nil)
	if err != nil {
		return err
	}
	d.currentPos = pos
	return nil
}

func (d *deltaWriter) SeekForward(pos uint64) error {
	d.currentPos += pos

	err := d.FlushBuffer()
	if err != nil {
		return err
	}

	err = d.writeOp(DeltaOpSeek, d.currentPos, nil)
	if err != nil {
		return err
	}
	return nil
}

func (d *deltaWriter) CopyFile(size uint64) error {
	err := d.FlushBuffer()
	if err != nil {
		return err
	}

	err = d.writeOp(DeltaOpCopy, size, nil)
	if err != nil {
		return err
	}
	d.currentPos += size
	return nil
}

func (d *deltaWriter) WriteAddContent(data []byte) error {
	err := d.FlushBuffer()
	if err != nil {
		return err
	}

	size := uint64(len(data))
	err = d.writeOp(DeltaOpAddData, size, data)
	if err != nil {
		return err
	}
	d.currentPos += size
	return nil
}

func (d *deltaWriter) CopyFileAt(offset uint64, size uint64) error {
	if err := d.Seek(offset); err != nil {
		return err
	}
	if err := d.CopyFile(size); err != nil {
		return err
	}
	return nil
}

func (d *deltaWriter) WriteOldFile(filename string, size uint64) error {
	err := d.SetCurrentFile(filename)
	if err != nil {
		return err
	}
	if err := d.Seek(0); err != nil {
		return err
	}
	err = d.CopyFile(size)
	if err != nil {
		return err
	}
	return nil
}

func (d *deltaWriter) Write(data []byte) (int, error) {
	n := len(data)
	err := d.WriteContent(data)
	return n, err
}

func maybeClose(closer io.Closer) {
	closer.Close()
}

func ApplyDelta(delta io.Reader, extractedDir string, dst io.Writer) error {
	buf := make([]byte, len(deltaHeader))
	_, err := io.ReadFull(delta, buf)
	if err != nil {
		return err
	}
	if !bytes.Equal(buf, deltaHeader[:]) {
		return fmt.Errorf("Invalid delta format")
	}

	decoder, err := zstd.NewReader(delta)
	if err != nil {
		return err
	}
	defer decoder.Close()

	r := bufio.NewReader(decoder)

	var currentFile *os.File
	defer maybeClose(currentFile)

	for {
		op, err := r.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		size, err := binary.ReadUvarint(r)
		if err != nil {
			return err
		}

		switch op {
		case DeltaOpData:
			_, err = io.CopyN(dst, r, int64(size))
			if err != nil {
				return err
			}
		case DeltaOpOpen:
			nameBytes := make([]byte, size)
			_, err = io.ReadFull(r, nameBytes)
			if err != nil {
				return err
			}
			name := string(nameBytes)
			path := extractedDir + "/" + name
			if currentFile != nil {
				currentFile.Close()
			}
			currentFile, err = os.Open(path)
			if err != nil {
				return err
			}
		case DeltaOpCopy:
			if currentFile == nil {
				return fmt.Errorf("No current file to copy from")
			}

			_, err = io.CopyN(dst, currentFile, int64(size))
			if err != nil {
				return err
			}
		case DeltaOpAddData:
			if currentFile == nil {
				return fmt.Errorf("No current file to copy from")
			}

			addBytes := make([]byte, size)
			_, err = io.ReadFull(r, addBytes)
			if err != nil {
				return err
			}

			addBytes2 := make([]byte, size)
			_, err = io.ReadFull(currentFile, addBytes2)
			if err != nil {
				return err
			}

			for i := uint64(0); i < size; i++ {
				addBytes[i] = addBytes[i] + addBytes2[i]
			}
			if _, err := dst.Write(addBytes); err != nil {
				return err
			}

		case DeltaOpSeek:
			if currentFile == nil {
				return fmt.Errorf("No current file to seek in")
			}
			_, err = currentFile.Seek(int64(size), 0)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("Unexpected delta op %d", op)
		}
	}

	return nil
}
