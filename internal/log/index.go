package log

import (
	"fmt"
	"io"
	"os"

	gommap "github.com/edsrzf/mmap-go"
)

var (
	offWidth uint64 = 4
	posWidth uint64 = 8
	entWidth        = offWidth + posWidth
)

type index struct {
	file *os.File
	mmap gommap.MMap
	size uint64
}

func newIndex(f *os.File, c Config) (*index, error) {
	idx := &index{
		file: f,
	}
	fi, err := os.Stat(f.Name())
	if err != nil {
		return nil, err
	}

	fmt.Println("tama no ", fi.Size())

	idx.size = uint64(fi.Size())
	if err = os.Truncate(
		f.Name(), int64(c.Segment.MaxIndexBytes),
	); err != nil {
		return nil, err
	}
	if idx.mmap, err = gommap.Map(
		idx.file,
		gommap.RDWR,
		0,
	); err != nil {
		return nil, err
	}
	return idx, nil
}

func (i *index) Close() error {
	// Desmapear el archivo antes de sincronizar y truncar
	/*if err := i.mmap.UnsafeUnmap(); err != nil {
		return fmt.Errorf("failed to unmap file: %w", err)
	}*/

	/*if err := i.mmap.Sync(gommap.MS_SYNC); err != nil {
		return err
	}*/
	i.mmap.Unmap()
	i.mmap.Flush()
	if err := i.file.Sync(); err != nil {
		return err
	}
	if err := i.file.Truncate(int64(i.size)); err != nil {
		return err
	}
	return i.file.Close()
}

func (i *index) Read(in int64) (out uint32, pos uint64, err error) {

	if i.size == 0 {
		return 0, 0, io.EOF
	}
	if in == -1 {
		fmt.Println("tiene ", i.size)
		out = uint32((i.size / entWidth) - 1)
		fmt.Println("OUT ", out)
	} else {
		out = uint32(in)
	}
	pos = uint64(out) * entWidth
	if i.size < pos+entWidth {
		return 0, 0, io.EOF
	}
	fmt.Println("postion ", pos)
	fmt.Println("postion 2 ", i.mmap[pos:pos+offWidth])
	out = enc.Uint32(i.mmap[pos : pos+offWidth])
	pos = enc.Uint64(i.mmap[pos+offWidth : pos+entWidth])
	return out, pos, nil
}

func (i *index) Write(off uint32, pos uint64) error {
	if uint64(len(i.mmap)) < i.size+entWidth {
		return io.EOF
	}
	enc.PutUint32(i.mmap[i.size:i.size+offWidth], off)
	enc.PutUint64(i.mmap[i.size+offWidth:i.size+entWidth], pos)
	i.size += uint64(entWidth)
	fmt.Println("sale con ", i.size)
	return nil
}

func (i *index) Name() string {
	return i.file.Name()
}
