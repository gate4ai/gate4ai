// SPDX-License-Identifier: FSL-1.1-ALv2

package daemon

import "encoding/json"

func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
