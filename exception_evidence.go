package debugbundle

import (
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/debugbundle/debugbundle-go/v3/internal/evidence"
)

type errorSnapshot struct {
	message string
	graph   map[string]any
}

// All Error/Unwrap callbacks share the existing single renderer slot and total
// two-millisecond capture wait. A stalled accessor never spawns a replacement.
func safeErrorSnapshot(err error, stack string, enabled, fatal bool) errorSnapshot {
	name := reflect.TypeOf(err).String()
	fallback := func(message string) errorSnapshot {
		var graph map[string]any
		if enabled {
			graph = unavailableErrorGraph(name, message)
		}
		return errorSnapshot{message, graph}
	}
	select {
	case errorRendererSlot <- struct{}{}:
	default:
		return fallback("[application error renderer busy]")
	}
	messages := make(chan string, 1)
	completed := make(chan errorSnapshot, 1)
	go func() {
		result := fallback("[application error message unavailable]")
		sentMessage := false
		defer func() {
			_ = recover()
			if !sentMessage {
				messages <- result.message
			}
			completed <- result
			<-errorRendererSlot
		}()
		result.message = err.Error()
		if len(result.message) > 4096 {
			result.message = result.message[:4096]
		}
		messages <- result.message
		sentMessage = true
		if enabled {
			result.graph = unavailableErrorGraph(name, result.message)
		}
		if enabled {
			result.graph = collectErrorEvidence(err, result.message, stack, fatal)
		}
	}()
	deadline := time.NewTimer(2 * time.Millisecond)
	defer deadline.Stop()
	result := fallback("[application error renderer timed out]")
	pendingMessages := messages
	for {
		select {
		case message := <-pendingMessages:
			result = fallback(message)
			pendingMessages = nil
		case complete := <-completed:
			return complete
		case <-deadline.C:
			select {
			case complete := <-completed:
				return complete
			default:
			}
			select {
			case message := <-pendingMessages:
				return fallback(message)
			default:
				return result
			}
		}
	}
}
func unavailableErrorGraph(name, message string) map[string]any {
	node := map[string]any{"id": float64(0), "name": clipEvidence(name, 256), "message": clipEvidence(message, 2048), "stack": nil,
		"trace_origin": "unavailable", "parsing_status": "unavailable", "frames": []any{}, "relationships": []any{}}
	return map[string]any{"version": float64(1), "runtime": "go", "root_id": float64(0), "nodes": []any{node}, "truncated": false, "unavailable_reason": "unsafe"}
}

type errorIdentity struct {
	kind    reflect.Type
	pointer uintptr
}
type errorGraphCollector struct {
	nodes               []any
	ids                 map[errorIdentity]int
	active              map[errorIdentity]bool
	heights             map[int]int
	maxNodes, maxFrames int
	fatal, truncated    bool
	unsafe              bool
}

func collectErrorEvidence(err error, message, stack string, fatal bool) map[string]any {
	collector := errorGraphCollector{nodes: []any{}, ids: map[errorIdentity]int{}, active: map[errorIdentity]bool{}, heights: map[int]int{}, maxNodes: 8, maxFrames: 32, fatal: fatal}
	if fatal {
		collector.maxNodes = 2
		collector.maxFrames = 8
	}
	collector.add(err, message, stack, 0, true)
	graph := map[string]any{"version": float64(1), "runtime": "go", "root_id": float64(0), "nodes": collector.nodes, "truncated": collector.truncated, "unavailable_reason": nil}
	if collector.unsafe {
		graph["unavailable_reason"] = "unsafe"
	}
	maximum := 31000
	if fatal {
		maximum = 3500
	}
	fitErrorEvidence(graph, maximum)
	return graph
}
func identityOf(err error) (errorIdentity, bool) {
	value := reflect.ValueOf(err)
	// Pointer identity is safe without comparing or traversing application fields.
	if value.Kind() != reflect.Pointer {
		return errorIdentity{}, false
	}
	return errorIdentity{value.Type(), value.Pointer()}, true
}
func (collector *errorGraphCollector) add(err error, message, stack string, depth int, root bool) (int, bool) {
	if err == nil {
		return 0, false
	}
	if depth > 4 {
		collector.truncated = true
		return 0, false
	}
	identity, hasIdentity := identityOf(err)
	if hasIdentity {
		if collector.active[identity] {
			collector.truncated = true
			return 0, false
		}
		if id, exists := collector.ids[identity]; exists {
			if depth+collector.heights[id] <= 4 {
				return id, true
			}
			collector.truncated = true
			return 0, false
		}
	}
	if len(collector.nodes) >= collector.maxNodes {
		collector.truncated = true
		return 0, false
	}
	id := len(collector.nodes)
	if hasIdentity {
		collector.ids[identity] = id
		collector.active[identity] = true
		defer delete(collector.active, identity)
	}
	if !root {
		var safe bool
		message, safe = nativeErrorMessage(err)
		if !safe {
			collector.unsafe = true
		}
	}
	name := reflect.TypeOf(err).String()
	frames, status := retainedGoFrames(stack, collector.maxFrames)
	if len(message) > 2048 || len(name) > 256 || status == "truncated" {
		collector.truncated = true
	}
	origin := "unavailable"
	if root && stack != "" {
		origin = "capture"
	}
	node := map[string]any{"id": float64(id), "name": clipEvidence(name, 256), "message": clipEvidence(message, 2048), "stack": nil,
		"trace_origin": origin, "parsing_status": status, "frames": frames, "relationships": []any{}}
	collector.nodes = append(collector.nodes, node)
	children, kind, safe, truncated := nativeErrorChildren(err)
	if !safe {
		collector.unsafe = true
	}
	if truncated {
		collector.truncated = true
	}
	for _, child := range children {
		childID, ok := collector.add(child, "", "", depth+1, false)
		if !ok {
			continue
		}
		edges := node["relationships"].([]any)
		duplicate := false
		for _, raw := range edges {
			edge := raw.(map[string]any)
			if edge["target_id"] == float64(childID) && edge["kind"] == kind {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		node["relationships"] = append(edges, map[string]any{"kind": kind, "target_id": float64(childID)})
		if height := collector.heights[childID] + 1; height > collector.heights[id] {
			collector.heights[id] = height
		}
	}
	return id, true
}
func nativeErrorMessage(err error) (message string, safe bool) {
	message = "[application error message unavailable]"
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	message = err.Error()
	return message, true
}
func nativeErrorChildren(err error) (children []error, kind string, safe, truncated bool) {
	safe = true
	kind = "cause"
	defer func() {
		if recover() != nil {
			children = nil
			safe = false
		}
	}()
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		raw := joined.Unwrap()
		kind = "aggregate"
		maximum := len(raw)
		if maximum > 5 {
			maximum = 5
			truncated = true
		}
		children = make([]error, maximum)
		copy(children, raw[:maximum])
		return
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if child := wrapped.Unwrap(); child != nil {
			children = []error{child}
		}
	}
	return
}
func retainedGoFrames(stack string, maximum int) ([]any, string) {
	frames := []any{}
	if stack == "" {
		return frames, "unavailable"
	}
	lines := strings.SplitN(stack, "\n", 130)
	status := "unparsed"
	for index := 1; index+1 < len(lines); index++ {
		source := lines[index+1]
		if !strings.HasPrefix(source, "\t") {
			continue
		}
		function := strings.TrimSpace(lines[index])
		if open := strings.LastIndex(function, "("); open >= 0 {
			function = function[:open]
		}
		if function == "" || strings.ContainsAny(function, " \t") {
			continue
		}
		location := strings.TrimSpace(source)
		if suffix := strings.LastIndex(location, " +0x"); suffix >= 0 {
			location = location[:suffix]
		}
		colon := strings.LastIndex(location, ":")
		if colon < 0 {
			continue
		}
		line, err := strconv.Atoi(location[colon+1:])
		if err != nil || line <= 0 {
			continue
		}
		if len(frames) >= maximum {
			return frames, "truncated"
		}
		classification := "unknown"
		if strings.HasPrefix(function, "github.com/debugbundle/debugbundle-go/") {
			classification = "sdk"
		} else if strings.HasPrefix(function, "runtime.") || strings.HasPrefix(function, "runtime/") {
			classification = "framework"
		}
		frames = append(frames, map[string]any{"function": clipEvidence(function, 256), "file": clipEvidence(location[:colon], 2048), "line": float64(line), "column": nil, "classification": classification})
		status = "parsed"
		index++
	}
	return frames, status
}
func clipEvidence(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}
func fitErrorEvidence(graph map[string]any, maximum int) {
	nodes := graph["nodes"].([]any)
	for evidence.Size(graph) > maximum {
		graph["truncated"] = true
		if len(nodes) > 1 {
			nodes = nodes[:len(nodes)-1]
			graph["nodes"] = nodes
			for _, raw := range nodes {
				node := raw.(map[string]any)
				edges := []any{}
				for _, edgeRaw := range node["relationships"].([]any) {
					if edgeRaw.(map[string]any)["target_id"].(float64) < float64(len(nodes)) {
						edges = append(edges, edgeRaw)
					}
				}
				node["relationships"] = edges
			}
			continue
		}
		root := nodes[0].(map[string]any)
		frames := root["frames"].([]any)
		if len(frames) > 0 {
			root["frames"] = frames[:len(frames)-1]
			root["parsing_status"] = "truncated"
			continue
		}
		message := root["message"].(string)
		if message != "" {
			root["message"] = clipEvidence(message, len(message)/2)
			continue
		}
		return
	}
}
