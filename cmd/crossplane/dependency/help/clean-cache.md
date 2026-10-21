The `clean-cache` command removes all cached package images from the local cache
directory. This can help free up disk space or resolve issues with corrupted
cache entries. The cache is shared by all projects, so each project downloads
the packages it needs again the next time it needs them.

The command doesn't remove generated schemas. Use `crossplane schema clean` for
that.
