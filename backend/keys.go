package backend

type KeyRetrieval interface {
	UserKeys(n string) (Keys, error)
}
