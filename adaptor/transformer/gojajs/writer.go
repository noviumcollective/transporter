package gojajs

import (
	"fmt"
	"time"

	"github.com/compose/mejson"
	"github.com/compose/transporter/client"
	"github.com/compose/transporter/log"
	"github.com/compose/transporter/message"
	"github.com/compose/transporter/message/data"
	"github.com/compose/transporter/message/ops"
	"github.com/dop251/goja"
)

var (
	_ client.Writer = &Writer{}
)

// Writer implements the client.Writer interface.
type Writer struct{}

func (w *Writer) Write(msg message.Msg) func(client.Session) error {
	return func(s client.Session) error {
		// short circuit for commands
		if msg.OP() == ops.Command {
			return nil
		}

		_, err := w.transformOne(s.(*Session), msg)
		return err
	}
}

func (w *Writer) transformOne(s *Session, msg message.Msg) (message.Msg, error) {
	var (
		outDoc goja.Value
		doc    interface{}
		err    error
	)

	now := time.Now().Nanosecond()
	currMsg := data.Data{
		"ts": msg.Timestamp(),
		"op": msg.OP().String(),
		"ns": msg.Namespace(),
	}

	curData := msg.Data()
	doc, err = mejson.Marshal(curData.AsMap())
	if err != nil {
		return msg, err
	}
	currMsg["data"] = doc

	// lets run our transformer on the document
	beforeVM := time.Now().Nanosecond()
	outDoc = s.fn(currMsg)

	var res map[string]interface{}
	if s.vm.ExportTo(outDoc, &res); err != nil {
		return msg, err
	}
	afterVM := time.Now().Nanosecond()
	newMsg, err := toMsg(s.vm, msg, res)
	if err != nil {
		// t.pipe.Err <- t.transformerError(adaptor.ERROR, err, msg)
		return msg, err
	}
	then := time.Now().Nanosecond()
	log.With("transformed_in_micro", (then-now)/1000).
		With("marshaled_in_micro", (beforeVM-now)/1000).
		With("vm_time_in_micro", (afterVM-beforeVM)/1000).
		With("unmarshaled_in_micro", (then-afterVM)/1000).
		Debugln("document transformed")

	return newMsg, nil
}

func toMsg(vm *goja.Runtime, origMsg message.Msg, incoming interface{}) (message.Msg, error) {
	var (
		op      ops.Op
		ts      = origMsg.Timestamp()
		ns      = origMsg.Namespace()
		mapData = origMsg.Data()
	)
	switch newMsg := incoming.(type) {
	case map[string]interface{}, data.Data: // we're a proper message.Msg, so copy the data over
		m := data.Data(newMsg.(map[string]interface{}))
		op = ops.OpTypeFromString(m.Get("op").(string))
		if op == ops.Skip {
			return nil, nil
		}
		ts = m.Get("ts").(int64)
		ns = m.Get("ns").(string)
		switch newData := m.Get("data").(type) {
		case goja.Value:
			var exported map[string]interface{}
			err := vm.ExportTo(newData, &exported)
			if err != nil {
				return nil, err
			}
			d, err := mejson.Unmarshal(exported)
			if err != nil {
				return nil, err
			}
			mapData = data.Data(d)
		case map[string]interface{}:
			newData, err := resolveValues(vm, newData)
			if err != nil {
				return nil, err
			}
			d, err := mejson.Unmarshal(newData)
			if err != nil {
				return nil, err
			}
			mapData = data.Data(d)
		case data.Data:
			newData, err := resolveValues(vm, newData)
			if err != nil {
				return nil, err
			}
			mapData = newData
		default:
			// this was setting the data directly instead of erroring before, recheck
			return nil, fmt.Errorf("bad type for data: %T", newData)
		}
	default: // something went wrong
		return nil, fmt.Errorf("returned doc was not a map[string]interface{}: was %T", newMsg)
	}
	msg := message.From(op, ns, mapData).(*message.Base)
	msg.TS = ts
	return msg, nil
}

func resolveValues(vm *goja.Runtime, m data.Data) (data.Data, error) {
	for k, v := range m {
		switch val := v.(type) {
		case goja.Value:
			inter := val.Export()
			if err := vm.ExportTo(val, &inter); err != nil {
				return nil, err
			}
			m[k] = inter
		}
	}
	return m, nil
}
