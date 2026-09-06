package rod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// JSONSerializationError describes a result that could not be serialized under
// the [Page.EvalJSON] contract.
type JSONSerializationError struct {
	Message string
}

func (e *JSONSerializationError) Error() string {
	return "serialize JavaScript result: " + e.Message
}

// EvalJSON evaluates a trusted JavaScript function, awaits its result, and
// decodes it into destination using encoding/json. It uses the same function,
// argument, context, and navigation retry conventions as [Page.Eval].
//
// A nil destination discards the awaited result without serializing it. Any
// other destination must be a non-nil pointer, validated before JavaScript runs.
//
// Serialization uses JSON.stringify with a replacer that rejects undefined,
// functions, symbols, bigints, and non-finite numbers, including visited nested
// values. Cycles and throwing serialization hooks also fail. Normal toJSON
// behavior applies; symbol-keyed and non-enumerable properties are not visited.
// Project Map, Set, Error, and DOM values into ordinary JSON data as needed.
//
// Errors distinguish destination validation, evaluation, serialization, and
// decoding. Wrapped evaluation and encoding/json errors preserve their causes.
// As with Eval, navigation retries do not guarantee exactly-once side effects.
func (p *Page) EvalJSON(destination any, js string, args ...any) error {
	if destination != nil {
		value := reflect.ValueOf(destination)
		if value.Kind() != reflect.Pointer || value.IsNil() {
			return fmt.Errorf("eval JSON destination: %w", &json.InvalidUnmarshalError{Type: value.Type()})
		}
	}

	fn := strings.Trim(js, "\t\n\v\f\r ;")
	wrapper := `async function() { await (` + fn + `).apply(this, arguments) }`
	if destination != nil {
		wrapper = `async function() {
			return (value => {
				try {
					return {json: JSON.stringify(value, (key, item) => {
						const type = typeof item;
						if (type === "undefined" || type === "function" ||
							type === "symbol" || type === "bigint" ||
							(type === "number" && !Number.isFinite(item))) {
							throw new TypeError("unsupported " + type + " at key " + JSON.stringify(key));
						}
						return item;
					})};
				} catch (error) {
					let message = "JSON serialization failed";
					try { message = String(error); } catch {}
					return {error: message};
				}
			})(await (` + fn + `).apply(this, arguments));
		}`
	}

	result, err := p.Eval(wrapper, args...)
	if err != nil {
		// EvalJSON exposes decoded values and error details, so an exception's
		// browser handle has no caller to release it. Cleanup remains possible
		// if the operation context expires after the exception was received.
		var evaluation *EvalError
		if errors.As(err, &evaluation) && evaluation.Exception != nil && evaluation.Exception.ObjectID != "" {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(p.ctx), time.Second)
			releaseErr := p.Context(ctx).Release(evaluation.Exception)
			cancel()
			if releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("release JavaScript exception: %w", releaseErr))
			} else {
				evaluation.Exception.ObjectID = ""
			}
		}
		return fmt.Errorf("evaluate JavaScript for JSON: %w", err)
	}
	if destination == nil {
		return nil
	}

	var serialized struct {
		JSON  string  `json:"json"`
		Error *string `json:"error"`
	}
	if err := result.Value.Unmarshal(&serialized); err != nil {
		return fmt.Errorf("read JavaScript JSON serialization result: %w", err)
	}
	if serialized.Error != nil {
		return &JSONSerializationError{Message: *serialized.Error}
	}
	if err := json.Unmarshal([]byte(serialized.JSON), destination); err != nil {
		return fmt.Errorf("decode JavaScript JSON into %T: %w", destination, err)
	}
	return nil
}
