package skills

import _ "embed"

// GossipSkillMD holds the canonical Gossip agent skill instructions embedded
// in the compiled binary so `gossip install` works standalone.
//go:embed gossip/SKILL.md
var GossipSkillMD []byte
