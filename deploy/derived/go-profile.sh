case ":${PATH}:" in
  *:/usr/local/go/bin:*) ;;
  *) PATH="/usr/local/go/bin:${PATH}" ;;
esac
export PATH
