package cpinterface

import (
	"code.google.com/p/go-uuid/uuid"
	"github.com/SoftwareDefinedBuildings/quasar"
	"github.com/SoftwareDefinedBuildings/quasar/qtree"
	capn "github.com/glycerine/go-capnproto"
	"github.com/op/go-logging"
	"net"
	"sync"
	"os/signal"
	"os"
)

var log *logging.Logger

func init() {
	log = logging.MustGetLogger("log")
}

type CPInterface struct {
	isShuttingDown bool
}

func ServeCPNP(q *quasar.Quasar, ntype string, laddr string) *CPInterface {
	rv := &CPInterface{}
	go func () {
		sigchan := make(chan os.Signal, 1)
		signal.Notify(sigchan, os.Interrupt)
		_ = <- sigchan
		rv.isShuttingDown = true
	} ()
	l, err := net.Listen(ntype, laddr)
	if err != nil {
		log.Panic(err)
	}
	defer l.Close()
	for !rv.isShuttingDown {
		conn, err := l.Accept()
		if err != nil {
			log.Panic(err)
		}
		go func(c net.Conn) {
			rv.dispatchCommands(q, c)
		}(conn)
	}
	return rv
}

func (c *CPInterface) Shutdown() {
	c.isShuttingDown = true
}

func (c *CPInterface) dispatchCommands(q *quasar.Quasar, conn net.Conn) {
	//This governs the stream
	rmtx := sync.Mutex{}
	wmtx := sync.Mutex{}
	log.Info("cpnp connection")
	for !c.isShuttingDown {
		rmtx.Lock()
		seg, err := capn.ReadFromStream(conn, nil)
		if err != nil {
			log.Warning("ERR (%v) :: %v", conn.RemoteAddr(), err)
			conn.Close()
			break
		}
		rmtx.Unlock()
		go func() {
			seg := seg
			req := ReadRootRequest(seg)
			rvseg := capn.NewBuffer(nil)
			resp := NewRootResponse(rvseg)
			resp.SetEchoTag(req.EchoTag())
			switch req.Which() {
			case REQUEST_QUERYSTANDARDVALUES:
				st := req.QueryStandardValues().StartTime()
				et := req.QueryStandardValues().EndTime()
				uuid := uuid.UUID(req.QueryStandardValues().Uuid())
				ver := req.QueryStandardValues().Version()
				log.Info("[REQ=QsV] st=%v, et=%v, uuid=%v, gen=%v", st, et, uuid, ver)
				if ver == 0 {
					ver = quasar.LatestGeneration
				}
				rv, gen, err := q.QueryValues(uuid, st, et, ver)
				switch err {
				case nil:
					resp.SetStatusCode(STATUSCODE_OK)
					records := NewRecords(rvseg)
					rl := NewRecordList(rvseg, len(rv))
					rla := rl.ToArray()
					if len(rla) != len(rv) {
						log.Critical("lenrv=%v lenrla=%v", len(rv), len(rla))
						log.Panicf("We got the weird condition")
					}
					for i, v := range rv {
						rla[i].SetTime(v.Time)
						rla[i].SetValue(v.Val)
					}
					records.SetVersion(gen)
					records.SetValues(rl)
					resp.SetRecords(records)
				default:
					log.Warning("RESPONDING ERR: %v", err)
					resp.SetStatusCode(STATUSCODE_INTERNALERROR)
					//TODO specialize this
				}
			case REQUEST_QUERYSTATISTICALVALUES:
				st := req.QueryStatisticalValues().StartTime()
				et := req.QueryStatisticalValues().EndTime()
				uuid := uuid.UUID(req.QueryStatisticalValues().Uuid())
				pw := req.QueryStatisticalValues().PointWidth()
				ver := req.QueryStatisticalValues().Version()
				if ver == 0 {
					ver = quasar.LatestGeneration
				}
				rv, gen, err := q.QueryStatisticalValues(uuid, st, et, ver, pw)
				switch err {
				case nil:
					resp.SetStatusCode(STATUSCODE_OK)
					srecords := NewStatisticalRecords(rvseg)
					rl := NewStatisticalRecordList(rvseg, len(rv))
					rla := rl.ToArray()
					for i, v := range rv {
						rla[i].SetTime(v.Time)
						rla[i].SetCount(v.Count)
						rla[i].SetMin(v.Min)
						rla[i].SetMean(v.Mean)
						rla[i].SetMax(v.Max)
					}
					srecords.SetVersion(gen)
					srecords.SetValues(rl)
					resp.SetStatisticalRecords(srecords)
				default:
					resp.SetStatusCode(STATUSCODE_INTERNALERROR)
				}
				resp.SetStatusCode(STATUSCODE_INTERNALERROR)
			case REQUEST_QUERYVERSION:
				//ul := req.
				ul := req.QueryVersion().Uuids()
				ull := ul.ToArray()
				rvers := NewVersions(rvseg)
				vlist := rvseg.NewUInt64List(len(ull))
				ulist := rvseg.NewDataList(len(ull))
				for i, v := range ull {
					ver, err := q.QueryGeneration(uuid.UUID(v))
					if err != nil {
						resp.SetStatusCode(STATUSCODE_INTERNALERROR)
						break
					}
					//I'm not sure that the array that sits behind the uuid slice will stick around
					//so I'm copying it.
					uuid := make([]byte, 16)
					copy(uuid, v)
					vlist.Set(i, ver)
					ulist.Set(i, uuid)
				}
				resp.SetStatusCode(STATUSCODE_OK)
				rvers.SetUuids(ulist)
				rvers.SetVersions(vlist)
				resp.SetVersionList(rvers)
			case REQUEST_QUERYNEARESTVALUE:
				t := req.QueryNearestValue().Time()
				id := uuid.UUID(req.QueryNearestValue().Uuid())
				ver := req.QueryNearestValue().Version()
				if ver == 0 {
					ver = quasar.LatestGeneration
				}
				back := req.QueryNearestValue().Backward()
				rv, gen, err := q.QueryNearestValue(id, t, back, ver)
				switch err {
				case nil:
					resp.SetStatusCode(STATUSCODE_OK)
					records := NewRecords(rvseg)
					rl := NewRecordList(rvseg, 1)
					rla := rl.ToArray()
					rla[0].SetTime(rv.Time)
					rla[0].SetValue(rv.Val)
					records.SetVersion(gen)
					records.SetValues(rl)
					resp.SetRecords(records)
				case qtree.ErrNoSuchPoint:
					resp.SetStatusCode(STATUSCODE_NOSUCHPOINT)
				default:
					resp.SetStatusCode(STATUSCODE_INTERNALERROR)
					//TODO specialize this
				}
			case REQUEST_QUERYCHANGEDRANGES:
				id := uuid.UUID(req.QueryChangedRanges().Uuid())
				sgen := req.QueryChangedRanges().FromGeneration()
				egen := req.QueryChangedRanges().ToGeneration()
				if egen == 0 {
					egen = quasar.LatestGeneration
				}
				thresh := req.QueryChangedRanges().Threshold()
				rv, ver, err := q.QueryChangedRanges(id, sgen, egen, thresh)
				switch err {
				case nil:
					resp.SetStatusCode(STATUSCODE_OK)
					ranges := NewRanges(rvseg)
					ranges.SetVersion(ver)
					crl := NewChangedRangeList(rvseg, len(rv))
					crla := crl.ToArray()
					for i := 0; i < len(rv); i++ {
						crla[i].SetStartTime(rv[i].Start)
						crla[i].SetEndTime(rv[i].End)
					}
					ranges.SetValues(crl)
					resp.SetChangedRngList(ranges)
				default:
					log.Critical("qcr error: ", err)
					resp.SetStatusCode(STATUSCODE_INTERNALERROR)
				}

			case REQUEST_INSERTVALUES:
				//log.Printf("GOT IV")
				uuid := uuid.UUID(req.InsertValues().Uuid())
				rl := req.InsertValues().Values()
				rla := rl.ToArray()
				if len(rla) != 0 {
					qtr := make([]qtree.Record, len(rla))
					for i, v := range rla {
						qtr[i] = qtree.Record{Time: v.Time(), Val: v.Value()}
					}
					q.InsertValues(uuid, qtr)
				}
				if req.InsertValues().Sync() {
					q.Flush(uuid)
				}
				resp.SetStatusCode(STATUSCODE_OK)
				//log.Printf("Responding OK")
			case REQUEST_DELETEVALUES:
				id := uuid.UUID(req.DeleteValues().Uuid())
				stime := req.DeleteValues().StartTime()
				etime := req.DeleteValues().EndTime()
				err := q.DeleteRange(id, stime, etime)
				switch err {
				case nil:
					resp.SetStatusCode(STATUSCODE_OK)
				default:
					resp.SetStatusCode(STATUSCODE_INTERNALERROR)
				}

			default:
				log.Critical("weird segment")
			}
			wmtx.Lock()
			rvseg.WriteTo(conn)
			wmtx.Unlock()
		}()
	}
}

/*
func EncodeMsg() *bytes.Buffer {
	rv := bytes.Buffer{}
	seg := capn.NewBuffer(nil)
	cmd := NewRootRequest(seg)

	qsv := NewCmdQueryStandardValues(seg)
	cmd.SetEchoTag(500)
	qsv.SetStartTime(0x5a5a)
	qsv.SetEndTime(0xf7f7)
	cmd.SetQueryStandardValues(qsv)
	seg.WriteTo(&rv)
	return &rv
}

func DecodeMsg(b *bytes.Buffer) {
	seg, err := capn.ReadFromStream(b, nil)
	if err != nil {
		log.Panic(err)
	}
	cmd := ReadRootRequest(seg)
	switch cmd.Which() {
	case REQUEST_QUERYSTANDARDVALUES:
		ca := cmd.QueryStandardValues()
	default:
		log.Critical("wtf")
	}
}
*/
