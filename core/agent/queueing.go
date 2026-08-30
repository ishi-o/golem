package agent

import "sync"

// queuedMessages is the message queue of one live run: a reply that arrives
// while the run is in flight joins it, read between tool calls, rather than
// waiting for a run of its own over the same ground.
//
// The producer is lazy — text arrives as a function, evaluated only when the
// message is actually read, because producing it may be work (a surface
// downloading message content, say) that a message never read should not
// pay for. A producer that fails contributes nothing: one message failing to
// join the run is answered by the re-fire, not by failing the run it
// intended to help.
type queuedMessages struct {
	mu      sync.Mutex
	entries []queuedEntry
	closed  bool
	// onRead is told which requests were read, for the listener surface.
	onRead func(requestIDs []string)
}

type queuedEntry struct {
	request Request
	// text produces the message body the first time it is needed.
	text func() string
	// display names the message for the queued notification (a preview, a
	// sender): identifying prose, not the body.
	display string
	// read marks a message taken into the run; a read message is never also
	// handed back by close.
	read bool
}

// offer enqueues a message on the run. False once the queue is closed: the
// model has stopped, so nothing can be read into the run any more and the
// message has to be answered some other way.
func (q *queuedMessages) offer(req Request, text func() string, display string) bool {
	if text == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.entries = append(q.entries, queuedEntry{request: req, text: text, display: display})
	return true
}

// read takes every unread message into the run, in arrival order, and
// forgets them: they are being answered by this run, so they must not also
// be handed back by close. An empty result is ordinary — most reads find
// nothing.
func (q *queuedMessages) read() []string {
	q.mu.Lock()
	var texts []string
	var ids []string
	for i := range q.entries {
		if q.entries[i].read {
			continue
		}
		text, err := safeText(q.entries[i].text)
		if err != nil || text == "" {
			// A producer that fails is dropped, not retried: close hands
			// back only what was never attempted, and a failed producer
			// would hand back a request whose body it cannot produce.
			q.entries[i].read = true
			continue
		}
		q.entries[i].read = true
		texts = append(texts, text)
		ids = append(ids, q.entries[i].request.RequestID)
	}
	q.mu.Unlock()
	if q.onRead != nil && len(ids) > 0 {
		q.onRead(ids)
	}
	return texts
}

// close seals the queue and hands back the messages the run never got round
// to reading: each is a message nobody has answered, so it is re-fired as
// the run it would have been. Synchronized with read so a message is read
// into the run or handed back, never both and never neither.
func (q *queuedMessages) close() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	var unread []Request
	for _, e := range q.entries {
		if !e.read {
			unread = append(unread, e.request)
		}
	}
	q.entries = nil
	return unread
}

func safeText(produce func() string) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text, err = "", errQueuedProducer
		}
	}()
	return produce(), nil
}

type queuedProducerError struct{}

func (queuedProducerError) Error() string { return "golem: queued message producer panicked" }

var errQueuedProducer error = queuedProducerError{}
