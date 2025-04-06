package schema

import (
	"encoding/json"
)

type RequestID struct {
	Value interface{}
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	var i interface{}
	if err := json.Unmarshal(data, &i); err != nil {
		return err
	}
	id.Value = i
	return nil
}

func (id *RequestID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.Value)
}

func RequestID_FromUInt64(value uint64) RequestID {
	return RequestID{Value: value}
}

func (id *RequestID) String() string {
	if id == nil || id.Value == nil {
		return "nil"
	}
	bytes, err := json.Marshal(id.Value)
	if err != nil {
		return err.Error()
	}
	return string(bytes)
}

func (id *RequestID) IsEmpty() bool {
	return id == nil || id.Value == nil
}
