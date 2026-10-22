# Debian's /etc/profile replaces the image ENV PATH for non-root login shells,
# so the cargo bin directory is re-added here the same way Deck does for its own tools.
case ":${PATH}:" in
  *:/usr/local/cargo/bin:*) ;;
  *) PATH="/usr/local/cargo/bin:${PATH}" ;;
esac
export PATH
export RUSTUP_HOME="${RUSTUP_HOME:-/usr/local/rustup}"
export CARGO_HOME="${CARGO_HOME:-/usr/local/cargo}"
