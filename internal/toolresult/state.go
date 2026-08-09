





package toolresult











type ContentReplacementState struct {
	SeenIDs      map[string]struct{}
	Replacements map[string]string
}



func New() *ContentReplacementState {
	return &ContentReplacementState{
		SeenIDs:      make(map[string]struct{}),
		Replacements: make(map[string]string),
	}
}





func (s *ContentReplacementState) Clone() *ContentReplacementState {
	out := &ContentReplacementState{
		SeenIDs:      make(map[string]struct{}, len(s.SeenIDs)),
		Replacements: make(map[string]string, len(s.Replacements)),
	}
	for k := range s.SeenIDs {
		out.SeenIDs[k] = struct{}{}
	}
	for k, v := range s.Replacements {
		out.Replacements[k] = v
	}
	return out
}

