package ai.alertd.imagethat.media

import okio.FileHandle
import okio.FileSystem
import okio.ForwardingFileSystem
import okio.IOException
import okio.Path
import okio.Sink

/**
 * The file system handed to Coil with decrypted thumbnail bytes. Reads pass through; every write
 * (sink, temp file, directory, move, delete, symlink) throws, so a decoder that asks
 * ImageSource.file() for a path fails instead of spilling plaintext into java.io.tmpdir (cacheDir).
 */
object ReadOnlyFileSystem : ForwardingFileSystem(FileSystem.SYSTEM) {
    private fun refuse(what: String, path: Path): Nothing =
        throw IOException("decrypted data must not be written to disk ($what $path)")

    override fun sink(file: Path, mustCreate: Boolean): Sink = refuse("sink", file)
    override fun appendingSink(file: Path, mustExist: Boolean): Sink = refuse("appendingSink", file)
    override fun openReadWrite(file: Path, mustCreate: Boolean, mustExist: Boolean): FileHandle = refuse("openReadWrite", file)
    override fun createDirectory(dir: Path, mustCreate: Boolean) = refuse("createDirectory", dir)
    override fun atomicMove(source: Path, target: Path) = refuse("atomicMove", target)
    override fun delete(path: Path, mustExist: Boolean) = refuse("delete", path)
    override fun createSymlink(source: Path, target: Path) = refuse("createSymlink", source)
}
