package harness

import (
	"context"
	"hash/fnv"
	"strconv"
)

// workerPool gives each conversation a serial execution lane: messages for
// the same (company, chat) always hash to the same worker and are processed
// in order, while different conversations — and different companies — run in
// parallel across workers. Bounded queues keep memory flat on the phone.
type workerPool struct {
	queues  []chan Inbound
	process func(context.Context, Inbound)
}

func newWorkerPool(n, queueSize int, process func(context.Context, Inbound)) *workerPool {
	if n < 1 {
		n = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	p := &workerPool{process: process}
	for i := 0; i < n; i++ {
		p.queues = append(p.queues, make(chan Inbound, queueSize))
	}
	return p
}

func (p *workerPool) start(ctx context.Context) {
	for _, q := range p.queues {
		q := q
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-q:
					p.process(ctx, msg)
				}
			}
		}()
	}
}

// dispatch routes a message to its conversation's worker without blocking
// the caller (the WhatsMeow event handler). Returns false if that worker's
// queue is full.
func (p *workerPool) dispatch(msg Inbound) bool {
	h := fnv.New32a()
	h.Write([]byte(strconv.FormatInt(msg.CompanyID, 10)))
	h.Write([]byte{'|'})
	h.Write([]byte(msg.ChatJID))
	q := p.queues[int(h.Sum32())%len(p.queues)]
	select {
	case q <- msg:
		return true
	default:
		return false
	}
}
