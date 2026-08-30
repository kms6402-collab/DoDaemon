package tftp

import "bytes"

// toNetascii converts local (LF) line endings to netascii's CRLF, per
// RFC 764, for data read off disk and sent to the client.
func toNetascii(b []byte) []byte {
	if !bytes.ContainsRune(b, '\n') {
		return b
	}
	out := make([]byte, 0, len(b)+bytes.Count(b, []byte{'\n'}))
	for _, c := range b {
		if c == '\n' {
			out = append(out, '\r', '\n')
		} else {
			out = append(out, c)
		}
	}
	return out
}

// fromNetascii converts incoming netascii CRLF sequences to a plain LF
// before writing to disk. A lone trailing CR is buffered by the caller
// across DATA packet boundaries via the carry return value.
func fromNetascii(b []byte, pendingCR bool) (out []byte, carryCR bool) {
	out = make([]byte, 0, len(b))
	i := 0
	if pendingCR && len(b) > 0 {
		if b[0] == '\n' {
			out = append(out, '\n')
			i = 1
		} else {
			out = append(out, '\r')
		}
	}
	for ; i < len(b); i++ {
		c := b[i]
		if c == '\r' {
			if i+1 < len(b) {
				if b[i+1] == '\n' {
					out = append(out, '\n')
					i++
					continue
				}
				out = append(out, '\r')
				continue
			}
			carryCR = true
			continue
		}
		out = append(out, c)
	}
	return out, carryCR
}
