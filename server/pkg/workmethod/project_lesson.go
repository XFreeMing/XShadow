package workmethod

import (
	"errors"
	"regexp"
	"strings"
)

// ProjectLesson is a reusable technique proposed by the final reviewer.
type ProjectLesson struct {
	Name        string   `json:"name"`
	When        string   `json:"when"`
	Steps       []string `json:"steps"`
	Verify      string   `json:"verify"`
	Avoid       string   `json:"avoid"`
	CriterionID string   `json:"criterion_id"`
	CaseID      string   `json:"case_id"`
}

var lessonName = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

func (l *ProjectLesson) Validate() error {
	if len(l.Name) > 40 || !lessonName.MatchString(l.Name) {
		return errors.New("project_lesson.name must be a kebab-case name of up to 40 characters")
	}
	if !validText(l.When, 480) || strings.ContainsAny(l.When, "\r\n") ||
		!validText(l.Verify, 1500) || !validText(l.Avoid, 1500) ||
		!validText(l.CriterionID, 80) || !validText(l.CaseID, 80) {
		return errors.New("project_lesson requires applicability, verification, a pitfall, and an acceptance criterion and case")
	}
	for _, text := range []string{l.When, l.Verify, l.Avoid, l.CriterionID, l.CaseID} {
		if strings.ContainsRune(text, 0) {
			return errors.New("project_lesson contains invalid text")
		}
	}
	if len(l.Steps) == 0 || len(l.Steps) > 8 {
		return errors.New("project_lesson requires one to eight reusable steps")
	}
	for _, step := range l.Steps {
		if !validText(step, 1500) || strings.ContainsRune(step, 0) {
			return errors.New("project_lesson.steps must contain nonempty steps of up to 1500 characters")
		}
	}
	return nil
}

func (r Run) CanProposeProjectLesson() bool {
	return r.State == "review" && r.Method != nil && r.Method.Kind == "development" &&
		r.Step >= 0 && r.Step+1 == len(r.Method.Steps) && r.Method.Steps[r.Step].Reviewer != nil
}

// ProjectLessonBrief is included in the existing review handoff.
const ProjectLessonBrief = `If independently verified work yielded a reusable technique for this project, include one optional project_lesson in your final approve JSON:
{"name":"short-stable-kebab-case","when":"when another task should use it","steps":["the reusable steps"],"verify":"observable check and expected result","avoid":"the concrete failed approach or boundary","criterion_id":"current acceptance criterion","case_id":"its passing test case"}
Derive it from the same candidate you verify. State the condition, the actual technique, and what it avoids. Omit project_lesson for routine one-off work, speculation, or absent reproducible evidence. Omit credentials, personal data, private inputs, and machine-specific paths. The platform stores it for project release review.`
