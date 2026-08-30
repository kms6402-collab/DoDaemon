package icons

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"io"
)

// EncodeICO writes a Windows .ico container holding one PNG-compressed
// frame per size (the PNG-in-ICO format Windows Vista and later support —
// see MS-ICO — which avoids hand-rolling BITMAPINFOHEADER/AND-mask DIBs).
func EncodeICO(w io.Writer, sizes []int) error {
	type frame struct {
		size int
		png  []byte
	}
	frames := make([]frame, 0, len(sizes))
	for _, s := range sizes {
		var buf bytes.Buffer
		if err := png.Encode(&buf, Draw(s)); err != nil {
			return err
		}
		frames = append(frames, frame{size: s, png: buf.Bytes()})
	}

	// ICONDIR header: reserved(2)=0, type(2)=1 (icon), count(2)
	if err := binary.Write(w, binary.LittleEndian, uint16(0)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(1)); err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint16(len(frames))); err != nil {
		return err
	}

	// ICONDIRENTRY is 16 bytes each; image data follows immediately after
	// all directory entries.
	offset := uint32(6 + 16*len(frames))
	for _, f := range frames {
		dim := byte(f.size)
		if f.size >= 256 {
			dim = 0 // 0 means 256 in the ICO format
		}
		entry := struct {
			Width, Height, ColorCount, Reserved byte
			Planes, BitCount                    uint16
			BytesInRes                          uint32
			ImageOffset                         uint32
		}{
			Width: dim, Height: dim, ColorCount: 0, Reserved: 0,
			Planes: 1, BitCount: 32,
			BytesInRes:  uint32(len(f.png)),
			ImageOffset: offset,
		}
		if err := binary.Write(w, binary.LittleEndian, entry); err != nil {
			return err
		}
		offset += uint32(len(f.png))
	}

	for _, f := range frames {
		if _, err := w.Write(f.png); err != nil {
			return err
		}
	}
	return nil
}
