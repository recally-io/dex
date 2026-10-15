package feishu

import (
	"encoding/binary"
	"errors"
)

// The wire fields follow the official SDK's ws/pbbp2 Frame. Unknown fields are skipped.
type frame struct {
	seq, log, service, method uint64
	headers                   []header
	encoding, kind, logNew    string
	payload                   []byte
}
type header struct{ key, value string }

func (f frame) get(key string) string {
	for _, h := range f.headers {
		if h.key == key {
			return h.value
		}
	}
	return ""
}
func number(b []byte, field, value uint64) []byte {
	b = binary.AppendUvarint(b, field<<3)
	return binary.AppendUvarint(b, value)
}
func blob(b []byte, field uint64, value []byte) []byte {
	b = binary.AppendUvarint(b, field<<3|2)
	b = binary.AppendUvarint(b, uint64(len(value)))
	return append(b, value...)
}
func (f frame) marshal() []byte {
	var b []byte
	b = number(b, 1, f.seq)
	b = number(b, 2, f.log)
	b = number(b, 3, f.service)
	b = number(b, 4, f.method)
	for _, h := range f.headers {
		b = blob(b, 5, blob(blob(nil, 1, []byte(h.key)), 2, []byte(h.value)))
	}
	if f.encoding != "" {
		b = blob(b, 6, []byte(f.encoding))
	}
	if f.kind != "" {
		b = blob(b, 7, []byte(f.kind))
	}
	b = blob(b, 8, f.payload)
	if f.logNew != "" {
		b = blob(b, 9, []byte(f.logNew))
	}
	return b
}

func fields(b []byte, accept func(uint64, uint64, []byte) error) error {
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 || key>>3 == 0 {
			return errors.New("invalid protobuf key")
		}
		b = b[n:]
		var value uint64
		var data []byte
		switch key & 7 {
		case 0:
			value, n = binary.Uvarint(b)
			if n <= 0 {
				return errors.New("invalid protobuf integer")
			}
			b = b[n:]
		case 1:
			if len(b) < 8 {
				return errors.New("short protobuf fixed64")
			}
			b = b[8:]
		case 2:
			size, n := binary.Uvarint(b)
			if n <= 0 {
				return errors.New("invalid protobuf length")
			}
			b = b[n:]
			if size > uint64(len(b)) {
				return errors.New("short protobuf field")
			}
			data = b[:size]
			b = b[size:]
		case 5:
			if len(b) < 4 {
				return errors.New("short protobuf fixed32")
			}
			b = b[4:]
		default:
			return errors.New("unsupported protobuf wire type")
		}
		if err := accept(key, value, data); err != nil {
			return err
		}
	}
	return nil
}
func parseFrame(b []byte) (frame, error) {
	var f frame
	err := fields(b, func(key, value uint64, data []byte) error {
		field := key >> 3
		if field <= 4 && key&7 != 0 || field >= 5 && field <= 9 && key&7 != 2 {
			return errors.New("invalid Frame wire type")
		}
		switch field {
		case 1:
			f.seq = value
		case 2:
			f.log = value
		case 3:
			f.service = value
		case 4:
			f.method = value
		case 5:
			var h header
			if err := fields(data, func(k, _ uint64, d []byte) error {
				if (k>>3 == 1 || k>>3 == 2) && k&7 != 2 {
					return errors.New("invalid Header wire type")
				}
				if k>>3 == 1 {
					h.key = string(d)
				}
				if k>>3 == 2 {
					h.value = string(d)
				}
				return nil
			}); err != nil {
				return err
			}
			f.headers = append(f.headers, h)
		case 6:
			f.encoding = string(data)
		case 7:
			f.kind = string(data)
		case 8:
			f.payload = data
		case 9:
			f.logNew = string(data)
		}
		return nil
	})
	return f, err
}
