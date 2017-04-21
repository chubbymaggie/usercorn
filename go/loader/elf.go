package loader

import (
	"bytes"
	"debug/dwarf"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"strings"

	"github.com/lunixbochs/usercorn/go/models"
)

var machineMap = map[elf.Machine]string{
	elf.EM_386:    "x86",
	elf.EM_X86_64: "x86_64",
	elf.EM_ARM:    "arm",
	elf.EM_MIPS:   "mips",
	elf.EM_PPC:    "ppc",
	elf.EM_PPC64:  "ppc64",
	elf.EM_SPARC:  "sparc",

	// TODO: if minimum version is bumped to Go 1.6, use the native enum elf.EM_AARCH64
	183: "arm64",
}

type ElfLoader struct {
	LoaderHeader
	file *elf.File
}

var elfMagic = []byte{0x7f, 0x45, 0x4c, 0x46}

func MatchElf(r io.ReaderAt) bool {
	return bytes.Equal(getMagic(r), elfMagic)
}

func NewElfLoader(r io.ReaderAt, arch string) (models.Loader, error) {
	file, err := elf.NewFile(r)
	if err != nil {
		return nil, err
	}
	var bits int
	switch file.Class {
	case elf.ELFCLASS32:
		bits = 32
	case elf.ELFCLASS64:
		bits = 64
	default:
		return nil, errors.New("Unknown ELF class.")
	}
	machineName, ok := machineMap[file.Machine]
	if !ok {
		return nil, fmt.Errorf("Unsupported machine: %s", file.Machine)
	}
	return &ElfLoader{
		LoaderHeader: LoaderHeader{
			arch:      machineName,
			bits:      bits,
			os:        "linux",
			entry:     file.Entry,
			byteOrder: file.ByteOrder,
		},
		file: file,
	}, nil
}

func (e *ElfLoader) Interp() string {
	for _, prog := range e.file.Progs {
		if prog.Type == elf.PT_INTERP {
			data, _ := ioutil.ReadAll(prog.Open())
			return strings.TrimRight(string(data), "\x00")
		}
	}
	return ""
}

func (e *ElfLoader) Header() (uint64, []byte, int) {
	for _, prog := range e.file.Progs {
		if prog.Type == elf.PT_PHDR {
			data := make([]byte, prog.Memsz)
			prog.Open().Read(data)
			return prog.Off, data, len(e.file.Progs)
		}
	}
	return 0, nil, 0
}

func (e *ElfLoader) Type() int {
	switch e.file.Type {
	case elf.ET_EXEC:
		return EXEC
	case elf.ET_DYN:
		return DYN
	default:
		return UNKNOWN
	}
}

func (e *ElfLoader) DataSegment() (start, end uint64) {
	sec := e.file.Section(".data")
	if sec != nil {
		return sec.Addr, sec.Addr + sec.Size
	}
	return 0, 0
}

func (e *ElfLoader) Segments() ([]models.SegmentData, error) {
	ret := make([]models.SegmentData, 0, len(e.file.Progs))
	for _, prog := range e.file.Progs {
		if prog.Type != elf.PT_LOAD {
			continue
		}
		filesz := prog.Filesz
		stream := prog.Open()
		prot := 0
		if prog.Flags&elf.PF_R != 0 {
			prot |= 1
		}
		if prog.Flags&elf.PF_W != 0 {
			prot |= 2
		}
		if prog.Flags&elf.PF_X != 0 {
			prot |= 4
		}
		ret = append(ret, models.SegmentData{
			Off:  prog.Off,
			Addr: prog.Vaddr,
			Size: prog.Memsz,
			Prot: prot,
			DataFunc: func() ([]byte, error) {
				data := make([]byte, filesz)
				_, err := stream.Read(data)
				// swallow EOF so we can still load broken binaries
				if err == io.EOF {
					err = nil
				}
				return data, err
			},
		})
	}
	return ret, nil
}

func (e *ElfLoader) getSymbols() ([]models.Symbol, error) {
	syms, err := e.file.Symbols()
	if err != nil {
		return []models.Symbol{}, err
	}
	symbols := make([]models.Symbol, 0, len(syms))
	for _, s := range syms {
		symbols = append(symbols, models.Symbol{
			Name:    s.Name,
			Start:   s.Value,
			End:     s.Value + s.Size,
			Dynamic: false,
		})
	}
	// don't care about missing dyn symtab
	dyn, _ := e.file.DynamicSymbols()
	for _, s := range dyn {
		symbols = append(symbols, models.Symbol{
			Name:    s.Name,
			Start:   s.Value,
			End:     s.Value + s.Size,
			Dynamic: true,
		})
	}
	return symbols, nil
}

func (e *ElfLoader) Symbols() ([]models.Symbol, error) {
	var err error
	if e.symCache == nil {
		e.symCache, err = e.getSymbols()
	}
	return e.symCache, err
}

func (e *ElfLoader) DWARF() (*dwarf.Data, error) {
	return e.file.DWARF()
}
