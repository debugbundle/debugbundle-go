package evidence

import "encoding/json"

// CompleteAcknowledgement separates a valid partial acknowledgement from proof
// that every submitted record was accepted. Unknown/legacy replies prove neither.
func CompleteAcknowledgement(body []byte, count int) (complete bool, invalid []int, valid bool) {
	if len(body) > MaxBodyBytes {
		return
	}
	var reply struct {
		Accepted *int `json:"accepted"`
		Rejected *int `json:"rejected"`
		Errors   []struct {
			Index  *int   `json:"index"`
			Reason string `json:"reason"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.Accepted == nil || reply.Rejected == nil || reply.Errors == nil {
		return
	}
	if *reply.Accepted < 0 || *reply.Rejected < 0 || *reply.Accepted+*reply.Rejected != count || len(reply.Errors) != *reply.Rejected {
		return
	}
	seen := map[int]bool{}
	for _, failure := range reply.Errors {
		if failure.Index == nil || *failure.Index < 0 || *failure.Index >= count || seen[*failure.Index] || failure.Reason == "" {
			return false, nil, false
		}
		seen[*failure.Index] = true
		if failure.Reason == "invalid_event" {
			invalid = append(invalid, *failure.Index)
		}
	}
	return *reply.Accepted == count, invalid, true
}
