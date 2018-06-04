package common

import (
	"fmt"
	"time"

	"github.com/bwmarrin/snowflake"
	"github.com/ryszard/goskiplist/skiplist"
)

type JOB interface {
}

type JobID struct {
	ID  int64 // unix time 毫秒
	SEQ int
}

func (j JobID) LessThan(other skiplist.Ordered) bool {
	if jo, ok := other.(JobID); ok {
		if j.ID == jo.ID {
			return j.SEQ < jo.SEQ
		} else {
			return j.ID < jo.ID
		}
	} else if jo, ok := other.(*JobID); ok {
		if j.ID == jo.ID {
			return j.SEQ < jo.SEQ
		} else {
			return j.ID < jo.ID
		}
	}
	return true

}

type Job struct {
	JobID
	interval time.Duration

	OutID int64

	//ch chan int64
	Retry    int
	MaxRetry int
	cb       func() bool
}

type TIMESCHED interface {
	AddCheckJob(time.Duration, func() bool) int64
	RemoveCheckJob(int64)
}

var TimeoutScheduleIns *TimeoutSchedule

type TimeoutSchedule struct {
	*skiplist.SkipList
	*snowflake.Node
	addCh    chan *Job
	removeCh chan struct {
		oid    int64
		ignore bool
	}
	index map[int64]*Job
	debug struct {
		d time.Duration
	}
}

func InitTimeoutSchedule() {
	idf, _ := snowflake.NewNode(100)
	TimeoutScheduleIns = &TimeoutSchedule{
		SkipList: skiplist.New(),
		Node:     idf,
		addCh:    make(chan *Job, 32),
		removeCh: make(chan struct {
			oid    int64
			ignore bool
		}, 32),
		index: map[int64]*Job{},
	}
	go TimeoutScheduleIns.Start()
}

func (ts *TimeoutSchedule) AddCheckJob(d time.Duration, cb func() bool) int64 {
	jd := JobID{
		ID: time.Now().Add(d).UnixNano() / 1e6,
		// 冲突项
		SEQ: 0,
	}
	outid := int64(ts.Generate())
	job := &Job{JobID: jd, interval: d, cb: cb, OutID: outid, MaxRetry: 2}
	ts.addCh <- job
	// JobID maybe modify
	// chan 是引用传递方式，所以可以这样使用
	//jd.SEQ = <-job.ch
	return outid
}

func (ts *TimeoutSchedule) RemoveCheckJob(jid int64) {
	ts.removeCh <- struct {
		oid    int64
		ignore bool
	}{jid, false}
}

type jobDone struct {
	*Job
	result bool
}

func (ts *TimeoutSchedule) process(clock *time.Timer, doWorks chan jobDone) {
	now := time.Now().UnixNano() / 1e6
	var dropItems []interface{}
	it := ts.Iterator()
	for it.Next() {
		if job, ok := it.Value().(*Job); ok {
			if t := job.ID - now; t <= 0 {
				if job.cb == nil {
					job.cb = func() bool { return true }
				}
				go func() {
					// 控制并发度，并处理结果, false 的要重新reschedule
					doWorks <- jobDone{job, job.cb()}
				}()
				dropItems = append(dropItems, it.Key())
			} else {
				// 重置timer会有很大的开销么，是否还不如定时轮询
				if !clock.Stop() {
					//<-clock.C
				}
				ts.debug.d = time.Duration(t * 1e6)
				clock.Reset(time.Duration(t * 1e6))
				break
			}

		} else {
			fmt.Errorf("type err: job")
			dropItems = append(dropItems, it.Key())
			continue
		}

	}
	// important 同步删除
	for _, key := range dropItems {
		ts.Delete(key)
	}
}
func (ts *TimeoutSchedule) Start() {
	clock := time.NewTimer(365 * 24 * time.Hour)
	doWorks := make(chan jobDone, 1024)
	go func() {
		for {
			select {
			case jd := <-doWorks:
				if !jd.result {
					jd.Job.Retry += 1
					fmt.Println("*************", jd.Job.Retry, jd.Job.MaxRetry, jd.Job.Retry <= jd.Job.MaxRetry)
					if jd.Job.Retry <= jd.Job.MaxRetry {
						jd.Job.JobID.ID = time.Now().Add((time.Duration)(int64(jd.Job.interval)*int64(jd.Job.Retry))).UnixNano() / 1e6

						ts.addCh <- jd.Job
					}

				} else {
					fmt.Println(time.Now(), jd.Job.OutID)
					ts.removeCh <- struct {
						oid    int64
						ignore bool
					}{jd.Job.OutID, true}
				}
			}
		}
	}()
	for {
		select {
		case outID := <-ts.removeCh:
			if outID.ignore {
				delete(ts.index, outID.oid)
			} else {
				if job, found := ts.index[outID.oid]; found {
					if _, ok := ts.Delete(job.JobID); ok {
					}
					delete(ts.index, outID.oid)
				}
			}
		case job := <-ts.addCh:
			if job.Retry > 0 {
				// 说明已经被主动删除了
				if _, found := ts.index[job.OutID]; !found {
					break
				}
			}
			it := ts.Seek(job.JobID)
			if it == nil {
				ts.Set(job.JobID, job)
			} else {
				preJob := it.Value().(*Job)
				seq := 0
				for preJob.ID == job.ID {
					seq = preJob.SEQ
					it.Next()
					preJob = it.Value().(*Job)
				}
				seq += 1
				job.SEQ = seq
				ts.Set(job.JobID, job)
			}
			ts.index[job.OutID] = job
		case <-clock.C:
			// 如果没有任何job了， reset 1year
			if ts.Len() <= 0 {
				fmt.Println("36555555")
				if !clock.Stop() {
					//<-clock.C
				}
				ts.debug.d = 365 * 24 * time.Hour
				clock.Reset(365 * 24 * time.Hour)
			}
		}
		ts.process(clock, doWorks)
		fmt.Println("monitor", len(ts.index), ts.Len(), ts.debug.d)
		//for k, v := range ts.index {
		//	fmt.Println(k, v)
		//}
		//fmt.Println("mapppp end")
	}
}
