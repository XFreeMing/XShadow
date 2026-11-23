package daemon

import (
	"github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/skillbundle"
)

func preparedWorkspaceSkills(skills []SkillData, preparedNames []string) []protocol.PreparedSkill {
	if len(skills) != len(preparedNames) {
		return nil
	}
	var prepared []protocol.PreparedSkill
	for i, item := range skills {
		if preparedNames[i] == "" || item.ID == "" || (item.Source != "" && item.Source != skillbundle.SourceWorkspace) {
			continue
		}
		files := make([]skill.VersionFile, 0, len(item.Files))
		for _, file := range item.Files {
			files = append(files, skill.VersionFile{Path: file.Path, Content: file.Content})
		}
		hash, err := skill.VersionHash(item.Name, item.Description, item.Content, files)
		if err != nil {
			continue
		}
		prepared = append(prepared, protocol.PreparedSkill{ID: item.ID, ContentHash: hash})
	}
	return prepared
}
