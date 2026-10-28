package ai.alertd.imagethat.data

import java.io.InputStream

/**
 * One browsable vault ([source], remote or local) plus the unlocked built-in
 * offline vault ([offline], null while it is locked or not created).
 */
class VaultRepository(private val source: VaultSource, private val offline: LocalSource?) {
    suspend fun list(path: String): List<Item> = source.list(path)
    suspend fun open(path: String, range: LongRange? = null): InputStream = source.open(path, range)
    suspend fun size(path: String): Long = source.size(path)
    suspend fun thumbnail(path: String, px: Int): ByteArray = source.thumbnail(path, px)
    suspend fun upload(dir: String, name: String, input: InputStream, length: Long) = source.upload(dir, name, input, length)
    suspend fun mkdir(path: String) = source.mkdir(path)
    suspend fun move(from: String, to: String) = source.move(from, to)
    suspend fun delete(path: String) = source.delete(path)

    suspend fun saveOffline(items: List<Item>, prefix: String): SaveReport {
        val dest = offline ?: throw SourceError.Other("offline vault is not unlocked")
        return saveOffline(source, items, dest, prefix)
    }
}
