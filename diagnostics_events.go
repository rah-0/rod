package rod

import (
	"crypto/sha256"
	"slices"

	"github.com/rah-0/rod/lib/proto"
)

func (d *PageDiagnostics) collect(msg *Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	switch msg.Method {
	case (proto.RuntimeConsoleAPICalled{}).ProtoEvent():
		var e proto.RuntimeConsoleAPICalled
		msg.Load(&e)
		if len(d.data.Console) >= d.options.MaxRecords {
			d.data.DroppedConsole++
			return
		}
		render := diagnosticText{limit: d.options.MaxTextBytes, depth: d.options.MaxPreviewDepth}
		for i, arg := range e.Args {
			if i > 0 {
				render.write(" ")
			}
			render.object(arg)
			if render.full {
				break
			}
		}
		d.data.Console = append(d.data.Console, ConsoleMessage{Type: proto.RuntimeConsoleAPICalledType(d.text(string(e.Type))), Text: render.String(), DiagnosticLocation: d.location(e.StackTrace)})
	case (proto.RuntimeExceptionThrown{}).ProtoEvent():
		var e proto.RuntimeExceptionThrown
		msg.Load(&e)
		if e.ExceptionDetails == nil {
			return
		}
		if len(d.data.PageErrors) >= d.options.MaxRecords {
			d.data.DroppedPageErrors++
			return
		}
		x := e.ExceptionDetails
		location := d.location(x.StackTrace)
		if x.URL != "" || x.ScriptID != "" || x.LineNumber > 0 || x.ColumnNumber > 0 {
			if x.URL != "" {
				location.URL = d.text(x.URL)
			}
			location.Line = max(0, x.LineNumber+1)
			location.Column = max(0, x.ColumnNumber+1)
		}
		render := diagnosticText{limit: d.options.MaxTextBytes, depth: d.options.MaxPreviewDepth}
		render.write(x.Text)
		if x.Exception != nil {
			render.write(": ")
			// Error descriptions include useful messages and stacks; previews often do not.
			if x.Exception.Description != "" {
				render.write(x.Exception.Description)
			} else {
				render.object(x.Exception)
			}
		}
		d.data.PageErrors = append(d.data.PageErrors, PageError{ExceptionID: x.ExceptionID, Text: render.String(), DiagnosticLocation: location})
	case (proto.RuntimeExceptionRevoked{}).ProtoEvent():
		var e proto.RuntimeExceptionRevoked
		msg.Load(&e)
		d.data.PageErrors = slices.DeleteFunc(d.data.PageErrors, func(x PageError) bool { return x.ExceptionID == e.ExceptionID })
	case (proto.NetworkRequestWillBeSent{}).ProtoEvent():
		var e proto.NetworkRequestWillBeSent
		msg.Load(&e)
		if e.RedirectResponse != nil && e.RedirectResponse.Status >= 400 {
			d.failure(ResourceFailure{RequestID: e.RequestID, URL: d.text(e.RedirectResponse.URL), Type: e.Type, Status: e.RedirectResponse.Status})
		}
		if e.Request == nil {
			return
		}
		if _, exists := d.requests[sha256.Sum256([]byte(e.RequestID))]; !exists && len(d.requests) >= d.options.MaxRequests {
			d.data.DroppedRequests++
			return
		}
		d.requests[sha256.Sum256([]byte(e.RequestID))] = diagnosticRequest{url: d.text(e.Request.URL), typ: proto.NetworkResourceType(d.text(string(e.Type)))}
	case (proto.NetworkResponseReceived{}).ProtoEvent():
		var e proto.NetworkResponseReceived
		msg.Load(&e)
		if e.Response == nil {
			return
		}
		r, exists := d.requests[sha256.Sum256([]byte(e.RequestID))]
		r.url, r.typ, r.status = d.text(e.Response.URL), proto.NetworkResourceType(d.text(string(e.Type))), e.Response.Status
		if r.status >= 400 && !r.failureDropped {
			r.failureDropped = !d.failure(ResourceFailure{RequestID: e.RequestID, URL: r.url, Type: r.typ, Status: r.status})
		}
		if exists || len(d.requests) < d.options.MaxRequests {
			d.requests[sha256.Sum256([]byte(e.RequestID))] = r
		} else {
			d.data.DroppedRequests++
		}
	case (proto.NetworkLoadingFinished{}).ProtoEvent():
		var e proto.NetworkLoadingFinished
		msg.Load(&e)
		delete(d.requests, sha256.Sum256([]byte(e.RequestID)))
	case (proto.NetworkLoadingFailed{}).ProtoEvent():
		var e proto.NetworkLoadingFailed
		msg.Load(&e)
		r := d.requests[sha256.Sum256([]byte(e.RequestID))]
		delete(d.requests, sha256.Sum256([]byte(e.RequestID)))
		if r.failureDropped {
			return
		}
		if e.Type != "" {
			r.typ = proto.NetworkResourceType(d.text(string(e.Type)))
		}
		reason := e.ErrorText
		if e.CorsErrorStatus != nil {
			reason += ": " + string(e.CorsErrorStatus.CorsError)
		}
		d.failure(ResourceFailure{RequestID: e.RequestID, URL: r.url, Type: r.typ, Status: r.status, ErrorText: d.text(reason), BlockedReason: e.BlockedReason, Canceled: e.Canceled})
	}
}

func (d *PageDiagnostics) failure(record ResourceFailure) bool {
	id := sha256.Sum256([]byte(record.RequestID))
	record.RequestID = proto.NetworkRequestID(d.text(string(record.RequestID)))
	record.Type = proto.NetworkResourceType(d.text(string(record.Type)))
	record.BlockedReason = proto.NetworkBlockedReason(d.text(string(record.BlockedReason)))
	for i := len(d.data.ResourceFailures) - 1; i >= 0; i-- {
		x := &d.data.ResourceFailures[i]
		if d.failureIDs[i] != id || (record.URL != "" && x.URL != record.URL) {
			continue
		}
		if record.URL != "" {
			x.URL = record.URL
		}
		if record.Type != "" {
			x.Type = record.Type
		}
		if record.Status != 0 {
			x.Status = record.Status
		}
		if record.ErrorText != "" {
			x.ErrorText = record.ErrorText
		}
		if record.BlockedReason != "" {
			x.BlockedReason = record.BlockedReason
		}
		x.Canceled = x.Canceled || record.Canceled
		return true
	}
	if len(d.data.ResourceFailures) >= d.options.MaxRecords {
		d.data.DroppedResourceFailures++
		return false
	}
	d.data.ResourceFailures = append(d.data.ResourceFailures, record)
	d.failureIDs = append(d.failureIDs, id)
	return true
}

func (d *PageDiagnostics) text(text string) string {
	render := diagnosticText{limit: d.options.MaxTextBytes}
	render.write(text)
	return render.String()
}

func (d *PageDiagnostics) location(stack *proto.RuntimeStackTrace) DiagnosticLocation {
	// CDP payloads cannot contain pointer cycles, but keep synthetic previews safe.
	for depth := 0; stack != nil && depth < 32; depth++ {
		for _, frame := range stack.CallFrames {
			if frame != nil {
				return DiagnosticLocation{URL: d.text(frame.URL), Line: max(0, frame.LineNumber+1), Column: max(0, frame.ColumnNumber+1)}
			}
		}
		stack = stack.Parent
	}
	return DiagnosticLocation{}
}
