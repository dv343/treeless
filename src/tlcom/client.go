package tlcom

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
	"treeless/src/tlcom/tlproto"
)

var brokerTick = time.Millisecond * 30

//Stores a DB operation result
type Result struct {
	Value []byte
	Err   error
}

//Conn is a DB TCP client connection
type Conn struct {
	conn            *net.TCPConn
	responseChannel chan ResponserMsg
	rchPool         sync.Pool
}

type ResponserMsg struct {
	mess    tlproto.Message
	timeout time.Duration
	rch     chan Result
}

//CreateConnection returns a new DB connection
func CreateConnection(addr string, onClose func()) (*Conn, error) {
	//log.Println("Dialing for new connection", taddr)
	/*taddr, errp := net.ResolveTCPAddr("tcp", addr)
	if errp != nil {
		return nil, errp
	}*/

	d := &net.Dialer{Timeout: 3 * time.Second}
	tcpconn, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}

	var c Conn
	c.conn = tcpconn.(*net.TCPConn)

	c.responseChannel = make(chan ResponserMsg, 1024)
	c.rchPool = sync.Pool{New: func() interface{} {
		return make(chan Result, 1)
	}}
	go broker(&c, onClose)

	return &c, nil
}

type timeoutMsg struct {
	t   time.Time
	tid uint32
}

func broker(c *Conn, onClose func()) {
	fromWorld := make(chan tlproto.Message, 1024)
	toWorld := tlproto.NewBufferedConn(c.conn, fromWorld)
	waits := make(map[uint32]chan<- Result)
	tid := uint32(0)
	pq := make([]timeoutMsg, 0, 64)
	ticker := time.NewTicker(time.Hour)
	tickerActivated := false
	onCloseCalled := false
	go func() {
		for {
			select {
			case now := <-ticker.C:
				for len(pq) > 0 {
					f := pq[0]
					if now.After(f.t) {
						//Timeout'ed
						w, ok := waits[f.tid]
						if ok {
							delete(waits, f.tid)
							//fmt.Println("Timeout" + fmt.Sprint("Local", c.conn.LocalAddr(), "Remote", c.conn.RemoteAddr()))
							w <- Result{nil, errors.New("Timeout" + fmt.Sprint("Local", c.conn.LocalAddr(), "Remote", c.conn.RemoteAddr()))}
						}
						pq = pq[1:]
					} else {
						break
					}
				}
				if len(pq) == 0 && tickerActivated {
					ticker.Stop()
					ticker = time.NewTicker(time.Hour)
				}
			case msg, ok := <-c.responseChannel:
				if !ok {
					for len(pq) > 0 {
						f := pq[0]
						w, ok := waits[f.tid]
						if ok {
							if w == nil {
								panic("w rch == nil")
							}
							delete(waits, f.tid)
							w <- Result{nil, errors.New("Connection closed => fast timeout" + fmt.Sprint("Local", c.conn.LocalAddr(), "Remote", c.conn.RemoteAddr()))}
						}
						pq = pq[1:]
					}
					//log.Println(errors.New(fmt.Sprint("Broker: Connection closed", "Local", c.conn.LocalAddr(), "Remote", c.conn.RemoteAddr())))
					close(toWorld)
					return
				}
				msg.mess.ID = tid
				if msg.timeout > 0 {
					//Send and *Recieve*
					if msg.rch == nil {
						panic("msg rch == nil")
					}

					if len(pq) == 0 && !tickerActivated {
						ticker.Stop()
						ticker = time.NewTicker(brokerTick)
					}

					tm := timeoutMsg{t: time.Now().Add(msg.timeout), tid: tid}

					inserted := false
					for i := len(pq) - 1; i >= 0; i-- {
						t := pq[i].t
						if t.Before(tm.t) {
							pq = append(pq, tm)
							copy(pq[i+2:], pq[i+1:])
							pq[i+1] = tm
							inserted = true
							break
						}
					}
					if inserted == false {
						pq = append(pq, tm)
						copy(pq[0:], pq[1:])
						pq[0] = tm
					}
					waits[tid] = msg.rch
				} else {
					if msg.rch != nil {
						panic("msg rch != nil")
					}
				}
				tid++
				toWorld <- msg.mess
			case m, ok := <-fromWorld:
				if !ok {
					//Connection closed
					for len(pq) > 0 {
						f := pq[0]
						w, ok := waits[f.tid]
						//fmt.Println(w, ok)
						if ok {
							if w == nil {
								panic("w rch == nil")
							}
							delete(waits, f.tid)
							w <- Result{nil, errors.New("Connection closed => fast timeout" + fmt.Sprint("Local", c.conn.LocalAddr(), "Remote", c.conn.RemoteAddr()))}
						}
						pq = pq[1:]
					}
					if !onCloseCalled {
						onClose()
						onCloseCalled = true
					}
					time.Sleep(brokerTick)
					continue
				}
				w, ok := waits[m.ID]
				if !ok {
					//Was timeout'ed
					continue
				}
				ch := w
				delete(waits, m.ID)
				//l.Remove(w.el)
				switch m.Type {
				case tlproto.OpGetResponse:
					ch <- Result{m.Value, nil}
				case tlproto.OpSetOK:
					ch <- Result{nil, nil}
				case tlproto.OpDelOK:
					ch <- Result{nil, nil}
				case tlproto.OpCASOK:
					ch <- Result{nil, nil}
				case tlproto.OpProtectOK:
					ch <- Result{nil, nil}
				case tlproto.OpGetConfResponse:
					ch <- Result{m.Value, nil}
				case tlproto.OpAddServerToGroupACK:
					ch <- Result{nil, nil}
				case tlproto.OpGetChunkInfoResponse:
					ch <- Result{m.Value, nil}
				case tlproto.OpTransferCompleted:
					ch <- Result{nil, nil}
				case tlproto.OpErr:
					ch <- Result{nil, errors.New("Response error: " + string(m.Value))}
				default:
					ch <- Result{nil, errors.New("Invalid response operation code: " + fmt.Sprint(m.Type))}
				}
			}
		}
	}()
}

//Close this connection
func (c *Conn) Close() {
	close(c.responseChannel)
}

func (c *Conn) send(opType tlproto.Operation, key, value []byte,
	timeout time.Duration) chan Result {
	if timeout == 0 {
		//Send-only
		var m ResponserMsg
		m.mess.Type = opType
		m.mess.Key = key
		m.mess.Value = value
		c.responseChannel <- m
		return nil
	}
	//Send and recieve
	var m ResponserMsg
	m.mess.Type = opType
	m.mess.Key = key
	m.mess.Value = value
	m.timeout = timeout
	m.rch = c.rchPool.Get().(chan Result)
	c.responseChannel <- m
	return m.rch
}

func (c *Conn) sendAndReceive(opType tlproto.Operation, key, value []byte,
	timeout time.Duration) Result {
	rch := c.send(opType, key, value, timeout)
	if rch == nil {
		return Result{}
	}
	return <-rch
}

type GetOperation struct {
	rch chan Result
	c   *Conn
}

type SetOperation struct {
	rch chan Result
	c   *Conn
}

type DelOperation struct {
	rch chan Result
	c   *Conn
}

type CASOperation struct {
	rch chan Result
	c   *Conn
}

func (g *GetOperation) Wait() Result {
	if g.rch == nil {
		return Result{nil, errors.New("Already returned")}
	}
	r := <-g.rch
	g.c.rchPool.Put(g.rch)
	g.rch = nil
	return r
}

func (g *SetOperation) Wait() error {
	if g.rch == nil {
		return errors.New("Already returned")
	}
	r := <-g.rch
	g.c.rchPool.Put(g.rch)
	g.rch = nil
	return r.Err
}

func (g *DelOperation) Wait() error {
	if g.rch == nil {
		return errors.New("Already returned")
	}
	r := <-g.rch
	g.c.rchPool.Put(g.rch)
	g.rch = nil
	return r.Err
}

func (g *CASOperation) Wait() error {
	if g.rch == nil {
		return errors.New("Already returned")
	}
	r := <-g.rch
	g.c.rchPool.Put(g.rch)
	g.rch = nil
	return r.Err
}

//Get the value of key
func (c *Conn) Get(key []byte, timeout time.Duration) GetOperation {
	if timeout <= 0 {
		panic("get timeout <=0")
	}
	ch := c.send(tlproto.OpGet, key, nil, timeout)
	if ch == nil {
		panic("xnil")
	}
	return GetOperation{rch: ch, c: c}
}

//Set a new key/value pair
func (c *Conn) Set(key []byte, value []byte, timeout time.Duration) SetOperation {
	ch := c.send(tlproto.OpSet, key, value, timeout)
	return SetOperation{rch: ch, c: c}
}

//Del deletes a key/value pair
func (c *Conn) Del(key []byte, value []byte, timeout time.Duration) DelOperation {
	ch := c.send(tlproto.OpDel, key, value, timeout)
	return DelOperation{rch: ch, c: c}
}

func (c *Conn) CAS(key []byte, value []byte, timeout time.Duration) CASOperation {
	if timeout <= 0 {
		panic("CAS timeout <=0")
	}
	ch := c.send(tlproto.OpCAS, key, value, timeout)
	return CASOperation{rch: ch, c: c}
}

//Transfer a chunk
func (c *Conn) Transfer(addr string, chunkID int) error {
	key, err := json.Marshal(chunkID)
	if err != nil {
		panic(err)
	}
	value := []byte(addr)
	r := c.sendAndReceive(tlproto.OpTransfer, key, value, 500*time.Millisecond)
	return r.Err
}

//GetAccessInfo request DB access info
func (c *Conn) GetAccessInfo() ([]byte, error) {
	r := c.sendAndReceive(tlproto.OpGetConf, nil, nil, 500*time.Millisecond)
	return r.Value, r.Err
}

//AddServerToGroup request to add this server to the server group
func (c *Conn) AddServerToGroup(addr string) error {
	key := []byte(addr)
	r := c.sendAndReceive(tlproto.OpAddServerToGroup, key, nil, 500*time.Millisecond)
	return r.Err
}

func (c *Conn) Protect(chunkID int) error {
	key := make([]byte, 4) //TODO static array
	binary.LittleEndian.PutUint32(key, uint32(chunkID))
	r := c.sendAndReceive(tlproto.OpProtect, key, nil, 500*time.Millisecond)
	if r.Err != nil {
		return r.Err
	}
	return nil
}

//GetChunkInfo request chunk info
func (c *Conn) GetChunkInfo(chunkID int) (size uint64, err error) {
	key := make([]byte, 4) //TODO static array
	binary.LittleEndian.PutUint32(key, uint32(chunkID))
	r := c.sendAndReceive(tlproto.OpGetChunkInfo, key, nil, 500*time.Millisecond)
	if r.Err != nil {
		return 0, r.Err
	}
	return binary.LittleEndian.Uint64(r.Value), nil
}
