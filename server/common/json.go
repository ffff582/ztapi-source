package common

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

var ErrMultipleJsonValues = errors.New("JSON input must contain one value")

type DuplicateJsonObjectMemberError struct {
	Name string
	Path []string
}

func (e *DuplicateJsonObjectMemberError) Error() string {
	return fmt.Sprintf("duplicate JSON object member %q", e.Name)
}

func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func UnmarshalJsonStr(data string, v any) error {
	return json.Unmarshal(StringToByteSlice(data), v)
}

func DecodeJson(reader io.Reader, v any) error {
	return json.NewDecoder(reader).Decode(v)
}

func DecodeJsonStrict(reader io.Reader, v any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	return ensureSingleJsonValue(decoder)
}

// RejectDuplicateJsonObjectMembers scans one JSON value without collapsing object members.
func RejectDuplicateJsonObjectMembers(reader io.Reader) error {
	decoder := json.NewDecoder(reader)
	duplicates := make([]DuplicateJsonObjectMemberError, 0, 1)
	if err := scanJsonValue(decoder, nil, &duplicates, true); err != nil {
		return err
	}
	return ensureSingleJsonValue(decoder)
}

// FindDuplicateJsonObjectMembers scans one JSON value and returns every
// duplicate member in document order without collapsing nested objects.
func FindDuplicateJsonObjectMembers(reader io.Reader) ([]DuplicateJsonObjectMemberError, error) {
	decoder := json.NewDecoder(reader)
	duplicates := make([]DuplicateJsonObjectMemberError, 0)
	if err := scanJsonValue(decoder, nil, &duplicates, false); err != nil {
		return nil, err
	}
	if err := ensureSingleJsonValue(decoder); err != nil {
		return nil, err
	}
	return duplicates, nil
}

func ensureSingleJsonValue(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err == nil {
		return ErrMultipleJsonValues
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func scanJsonValue(decoder *json.Decoder, path []string, duplicates *[]DuplicateJsonObjectMemberError, stopOnDuplicate bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		members := map[string]struct{}{}
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("JSON object member must be a string")
			}
			if _, exists := members[name]; exists {
				duplicate := DuplicateJsonObjectMemberError{Name: name, Path: appendPath(path, name)}
				if stopOnDuplicate {
					return &duplicate
				}
				*duplicates = append(*duplicates, duplicate)
			} else {
				members[name] = struct{}{}
			}
			if err := scanJsonValue(decoder, appendPath(path, name), duplicates, stopOnDuplicate); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("invalid JSON object closing delimiter")
		}
	case '[':
		for decoder.More() {
			if err := scanJsonValue(decoder, path, duplicates, stopOnDuplicate); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("invalid JSON array closing delimiter")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func appendPath(path []string, name string) []string {
	next := make([]string, len(path)+1)
	copy(next, path)
	next[len(path)] = name
	return next
}

func Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func GetJsonType(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return "unknown"
	}
	firstChar := trimmed[0]
	switch firstChar {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// JsonRawMessageToString returns JSON strings as their decoded value and other JSON values as raw text.
func JsonRawMessageToString(data json.RawMessage) string {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] != '"' {
		return string(trimmed)
	}
	var value string
	if err := Unmarshal(trimmed, &value); err != nil {
		return string(trimmed)
	}
	return value
}
