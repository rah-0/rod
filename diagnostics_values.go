package rod

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rah-0/rod/lib/proto"
)

type diagnosticText struct {
	text  strings.Builder
	limit int
	depth int
	full  bool
}

func (r *diagnosticText) write(text string) {
	if r.full {
		return
	}
	left := r.limit - r.text.Len()
	if len(text) > left {
		text = text[:left]
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		r.full = true
	}
	r.text.WriteString(text)
}

func (r *diagnosticText) String() string {
	text := r.text.String()
	if !r.full {
		return text
	}
	marker := strings.Repeat(".", min(3, r.limit))
	if len(text) > r.limit-len(marker) {
		text = text[:r.limit-len(marker)]
	}
	for len(text) > 0 && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + marker
}

func (r *diagnosticText) quote(text string) {
	// Bound the input before quoting, which can expand control characters.
	if len(text) > r.limit {
		text = text[:r.limit]
		r.write(strconv.Quote(text))
		r.full = true
		return
	}
	r.write(strconv.Quote(text))
}

func (r *diagnosticText) object(object *proto.RuntimeRemoteObject) {
	if object == nil {
		r.write("undefined")
		return
	}
	if value := object.Value.Val(); value != nil {
		if text, ok := value.(string); ok {
			r.write(text)
		} else if encoded, err := json.Marshal(value); err == nil {
			r.write(string(encoded))
		} else {
			r.write(string(object.Type))
		}
		return
	}
	if object.UnserializableValue != "" {
		r.write(string(object.UnserializableValue))
		return
	}
	if object.Preview != nil {
		r.preview(object.Preview, 0)
		return
	}
	if object.Description != "" {
		r.write(object.Description)
		return
	}
	if object.Subtype == proto.RuntimeRemoteObjectSubtypeNull {
		r.write("null")
		return
	}
	r.write(string(object.Type))
}

func (r *diagnosticText) preview(preview *proto.RuntimeObjectPreview, depth int) {
	if r.full {
		return
	}
	if preview == nil {
		r.write("undefined")
		return
	}
	if depth >= r.depth {
		r.write("...")
		return
	}
	if preview.Type == proto.RuntimeObjectPreviewTypeString {
		r.quote(preview.Description)
		return
	}
	if preview.Type != proto.RuntimeObjectPreviewTypeObject {
		if preview.Description != "" {
			r.write(preview.Description)
		} else {
			r.write(string(preview.Type))
		}
		return
	}
	if preview.Subtype == proto.RuntimeObjectPreviewSubtypeNull {
		r.write("null")
		return
	}
	if preview.Subtype == proto.RuntimeObjectPreviewSubtypeMap || preview.Subtype == proto.RuntimeObjectPreviewSubtypeSet || len(preview.Entries) > 0 {
		name := "Map"
		if preview.Subtype == proto.RuntimeObjectPreviewSubtypeSet {
			name = "Set"
		}
		r.write(name + "{")
		count := 0
		for _, entry := range preview.Entries {
			if entry == nil {
				continue
			}
			if count > 0 {
				r.write(", ")
			}
			if entry.Key != nil {
				r.preview(entry.Key, depth+1)
				r.write(" => ")
			}
			r.preview(entry.Value, depth+1)
			count++
			if r.full {
				return
			}
		}
		if preview.Overflow {
			if count > 0 {
				r.write(", ")
			}
			r.write("...")
		}
		r.write("}")
		return
	}
	if len(preview.Properties) == 0 && !preview.Overflow && preview.Description != "" {
		r.write(preview.Description)
		return
	}
	r.write("{")
	count := 0
	for _, property := range preview.Properties {
		if property == nil {
			continue
		}
		if count > 0 {
			r.write(", ")
		}
		r.write(property.Name)
		r.write(": ")
		switch {
		case property.ValuePreview != nil:
			r.preview(property.ValuePreview, depth+1)
		case property.Type == proto.RuntimePropertyPreviewTypeString:
			r.quote(property.Value)
		case property.Value != "":
			r.write(property.Value)
		case property.Subtype == proto.RuntimePropertyPreviewSubtypeNull:
			r.write("null")
		default:
			r.write(string(property.Type))
		}
		count++
		if r.full {
			return
		}
	}
	if preview.Overflow {
		if count > 0 {
			r.write(", ")
		}
		r.write("...")
	}
	r.write("}")
}
