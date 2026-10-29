package ai.alertd.imagethat.keys

import java.io.File
import java.io.FileOutputStream
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import javax.crypto.AEADBadTagException
import kotlinx.serialization.SerializationException
import kotlinx.serialization.json.Json

interface SecretCipher {
    fun encrypt(plain: ByteArray): ByteArray
    fun decrypt(blob: ByteArray): ByteArray
}

/** Encrypted, atomically written [AppState] file. */
class StateStore(private val file: File, private val cipher: SecretCipher) {
    private val json = Json { ignoreUnknownKeys = true }

    @Synchronized
    fun load(): AppState {
        if (!file.exists()) return AppState()
        return try {
            json.decodeFromString(AppState.serializer(), String(cipher.decrypt(file.readBytes()), Charsets.UTF_8))
        } catch (e: Exception) {
            // Only a genuinely unreadable file is quarantined. Keystore or I/O
            // failures may be transient and the file fine, so they propagate.
            if (e !is AEADBadTagException && e !is SerializationException && e !is IllegalArgumentException) throw e
            // Keep the unreadable file for inspection, never overwriting an earlier one.
            val corrupt = File(file.parentFile, file.name + ".corrupt-" + System.currentTimeMillis())
            Files.move(file.toPath(), corrupt.toPath(), StandardCopyOption.REPLACE_EXISTING)
            AppState()
        }
    }

    @Synchronized
    fun update(block: (AppState) -> AppState) {
        val bytes = cipher.encrypt(json.encodeToString(AppState.serializer(), block(load())).toByteArray(Charsets.UTF_8))
        val tmp = File(file.parentFile, file.name + ".tmp")
        FileOutputStream(tmp).use { it.write(bytes); it.fd.sync() }
        Files.move(tmp.toPath(), file.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
    }
}
