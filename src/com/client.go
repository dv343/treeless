package tlcom

import (
	"container/list"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
	"treeless/src/com/tcp"
)

//Stores a DB operation result
type result struct {
	value []byte
	err   error
}

const tickerReserveSize = 32

//Conn is a DB TCP client connection
type Conn struct {
	Addr            string
	conn            net.Conn           //TCP connection
	writeChannel    chan tlTCP.Message //TCP writer communicattion is made throught this channel
	chanPool        sync.Pool          //Pool of channels to be used as mechanisms to wait until response, make a pool to avoid GC performance penalties
	mutex           sync.Mutex         //REmove, use atomic isClosed
	isClosed        bool
	tickerReserve   [tickerReserveSize]*time.Ticker
	reservedTickers int
	responseChannel chan ResponserMsg
}

type ResponserMsg struct {
	mess    tlTCP.Message
	timeout time.Duration
	rch     chan result
}

//CreateConnection returns a new DB connection
func CreateConnection(addr string) (*Conn, error) {
	//log.Println("Dialing for new connection", taddr)
	taddr, errp := net.ResolveTCPAddr("tcp", addr)
	if errp != nil {
		return nil, errp
	}
	tcpconn, err := net.DialTCP("tcp", nil, taddr)
	if err != nil {
		return nil, err
	}

	var c Conn
	c.Addr = addr
	c.conn = tcpconn
	c.chanPool.New = func() interface{} {
		return make(chan result)
	}
	c.writeChannel = make(chan tlTCP.Message, 8)

	c.responseChannel = make(chan ResponserMsg, 1024)

	go tlTCP.Writer(tcpconn, c.writeChannel)
	go Responser(&c)

	return &c, nil
}

type timeoutMsg struct {
	t   time.Time
	tid uint32
}
type waiter struct {
	r  chan<- result
	el *list.Element
}

func Responser(c *Conn) {
	readChannel := make(chan tlTCP.Message, 1024)
	waits := make(map[uint32]waiter)
	tid := uint32(0)
	l := list.New()
	ticker := time.NewTicker(time.Millisecond * 10)
	go func() {
		for {
			select {
			case <-ticker.C:
				for l.Len() > 0 {
					el := l.Front()
					f := el.Value.(timeoutMsg)
					now := time.Now()
					if now.After(f.t) {
						//Timeout'ed
						w := waits[f.tid]
						delete(waits, f.tid)
						w.r <- result{nil, errors.New("Timeout")}
						l.Remove(el)
					} else {
						break
					}
				}
			case msg, ok := <-c.responseChannel:
				if !ok {
					for l.Len() > 0 {
						el := l.Front()
						l.Remove(el)
						f := el.Value.(timeoutMsg)
						w := waits[f.tid]
						w.r <- result{nil, errors.New("Connection closed")}
					}
					close(c.writeChannel) //TODO make writechannel private
					return
				}
				msg.mess.ID = tid
				//TODO: opt dont call time.now
				tm := timeoutMsg{t: time.Now().Add(msg.timeout), tid: tid}
				var inserted *list.Element
				for el := l.Back(); el != l.Front(); el = el.Prev() {
					t := el.Value.(timeoutMsg).t
					if t.Before(tm.t) {
						inserted = l.InsertAfter(tm, el)
						break
					}
				}
				if inserted == nil {
					inserted = l.PushFront(tm)
				}

				waits[tid] = waiter{r: msg.rch, el: inserted}
				tid++
				c.writeChannel <- msg.mess
			case m := <-readChannel:
				w, ok := waits[m.ID]
				if !ok {
					//Was timeout'ed
					continue
				}
				ch := w.r
				delete(waits, m.ID)
				l.Remove(w.el)
				switch m.Type {
				case tlTCP.OpGetResponse:
					ch <- result{m.Value, nil}
				case tlTCP.OpSetOK:
					ch <- result{nil, nil}
				case tlTCP.OpDelOK:
					ch <- result{nil, nil}
				case tlTCP.OpGetConfResponse:
					ch <- result{m.Value, nil}
				case tlTCP.OpAddServerToGroupACK:
					ch <- result{nil, nil}
				case tlTCP.OpGetChunkInfoResponse:
					ch <- result{m.Value, nil}
				case tlTCP.OpTransferCompleted:
					ch <- result{nil, nil}
				case tlTCP.OpErr:
					ch <- result{nil, errors.New("Response error: " + string(m.Value))}
				default:
					ch <- result{nil, errors.New("Invalid response operation code: " + fmt.Sprint(m.Type))}
				}
			}
		}
	}()
	tlTCP.Reader(c.conn, readChannel)
	//log.Println("Connection closed", c.conn.RemoteAddr().String())
}

//Close this connection
func (c *Conn) Close() {
	if c != nil {
		c.mutex.Lock()
		if c.conn != nil {
			c.conn.Close()
			if !c.isClosed {
				close(c.responseChannel)
			}
		}
		c.isClosed = true
		c.mutex.Unlock()
	}
}

func (c *Conn) IsClosed() bool {
	c.mutex.Lock()
	closed := c.isClosed
	c.mutex.Unlock()
	return closed
}

//TODO timeout
func (c *Conn) sendAndRecieve(opType tlTCP.Operation, key, value []byte, timeout time.Duration) result {
	if c.IsClosed() {
		return result{nil, errors.New("Connection closed")}
	}
	var m ResponserMsg
	m.mess.Type = opType
	m.mess.Key = key
	m.mess.Value = value
	m.timeout = timeout
	m.rch = make(chan result) //c.chanPool.Get().(chan result)
	c.responseChannel <- m
	r := <-m.rch
	c.chanPool.Put(m.rch)
	return r
}

//Get the value of key
func (c *Conn) Get(key []byte) ([]byte, error) {
	r := c.sendAndRecieve(tlTCP.OpGet, key, nil, 100*time.Millisecond)
	return r.value, r.err
}

//Set a new key/value pair
func (c *Conn) Set(key, value []byte) error {
	r := c.sendAndRecieve(tlTCP.OpSet, key, value, 100*time.Millisecond)
	return r.err
}

//Del deletes a key/value pair
func (c *Conn) Del(key []byte) error {
	r := c.sendAndRecieve(tlTCP.OpDel, key, nil, 100*time.Millisecond)
	return r.err
}

//Transfer a chunk
func (c *Conn) Transfer(addr string, chunkID int) error {
	key, err := json.Marshal(chunkID)
	if err != nil {
		panic(err)
	}
	value := []byte(addr)
	r := c.sendAndRecieve(tlTCP.OpTransfer, key, value, 5*time.Second)
	return r.err
}

//GetAccessInfo request DB access info
func (c *Conn) GetAccessInfo() ([]byte, error) {
	r := c.sendAndRecieve(tlTCP.OpGetConf, nil, nil, 500*time.Millisecond)
	return r.value, r.err
}

//AddServerToGroup request to add this server to the server group
func (c *Conn) AddServerToGroup(addr string) error {
	key := []byte(addr)
	r := c.sendAndRecieve(tlTCP.OpAddServerToGroup, key, nil, 500*time.Millisecond)
	return r.err
}

//GetChunkInfo request chunk info
func (c *Conn) GetChunkInfo(chunkID int) (size uint64, err error) {
	key := make([]byte, 4) //TODO static array
	binary.LittleEndian.PutUint32(key, uint32(chunkID))
	r := c.sendAndRecieve(tlTCP.OpGetChunkInfo, key, nil, 500*time.Millisecond)
	if r.err != nil {
		return 0, r.err
	}
	return binary.LittleEndian.Uint64(r.value), nil
}
