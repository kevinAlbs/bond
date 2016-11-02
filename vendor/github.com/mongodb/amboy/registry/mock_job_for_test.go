package registry

// This file has a mock implementation of a job. Used in other tests.

import (
	"errors"
	"fmt"

	"github.com/mongodb/amboy"
	"github.com/mongodb/amboy/dependency"
)

func init() {
	AddJobType("test", jobTestFactory)
}

type JobTest struct {
	Name       string
	Content    string
	complete   bool
	shouldFail bool
	T          amboy.JobType
	dep        dependency.Manager
	priority   int
}

func NewTestJob(content string) *JobTest {
	id := fmt.Sprintf("%s-%s", content+"-job", content)

	return &JobTest{
		Name:    id,
		Content: content,
		dep:     dependency.NewAlways(),
		T: amboy.JobType{
			Name:    "test",
			Format:  amboy.BSON,
			Version: 0,
		},
	}
}

func jobTestFactory() amboy.Job {
	return &JobTest{
		T: amboy.JobType{
			Name:    "test",
			Format:  amboy.BSON,
			Version: 0,
		},
	}
}

func (j *JobTest) ID() string {
	return j.Name
}

func (j *JobTest) Run() {
	j.complete = true
}

func (j *JobTest) Error() error {
	if j.shouldFail {
		return errors.New("poisoned task")
	}

	return nil
}

func (j *JobTest) Completed() bool {
	return j.complete
}

func (j *JobTest) Type() amboy.JobType {
	return j.T
}

func (j *JobTest) Dependency() dependency.Manager {
	return j.dep
}

func (j *JobTest) SetDependency(d dependency.Manager) {
	j.dep = d
}

func (j *JobTest) Priority() int {
	return j.priority
}

func (j *JobTest) SetPriority(p int) {
	j.priority = p
}
