import com.android.build.api.artifact.SingleArtifact

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.kotlin.serialization)
}

// Release version comes from the tag via VERSION_NAME (x.y.z); local and CI
// debug builds fall back to a dev version. versionCode = major*10000 +
// minor*100 + patch, so 1.2.3 -> 10203 and it only ever grows with the tag.
val releaseVersionName = System.getenv("VERSION_NAME")?.takeIf { it.isNotBlank() } ?: "0.0.0-dev"
val releaseVersionCode = Regex("""^(\d+)\.(\d+)\.(\d+)""").find(releaseVersionName)
    ?.destructured
    ?.let { (major, minor, patch) -> major.toInt() * 10000 + minor.toInt() * 100 + patch.toInt() }
    ?: 1

// Release signing material comes only from the environment (CI secrets or a
// local export); nothing about the key lives in the repo.
val signingEnv = listOf("ANDROID_KEYSTORE_FILE", "ANDROID_KEYSTORE_PASSWORD", "ANDROID_KEY_ALIAS", "ANDROID_KEY_PASSWORD")
    .associateWith { System.getenv(it).orEmpty() }

android {
    namespace = "ai.alertd.imagethat"
    compileSdk = 35

    defaultConfig {
        applicationId = "ai.alertd.imagethat"
        minSdk = 29
        targetSdk = 35
        versionCode = maxOf(releaseVersionCode, 1)
        versionName = releaseVersionName
        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk { abiFilters += listOf("arm64-v8a", "x86_64") }
    }

    signingConfigs {
        create("release") {
            if (signingEnv.values.none { it.isEmpty() }) {
                storeFile = file(signingEnv.getValue("ANDROID_KEYSTORE_FILE"))
                storePassword = signingEnv.getValue("ANDROID_KEYSTORE_PASSWORD")
                keyAlias = signingEnv.getValue("ANDROID_KEY_ALIAS")
                keyPassword = signingEnv.getValue("ANDROID_KEY_PASSWORD")
            }
        }
    }

    buildTypes {
        release { signingConfig = signingConfigs.getByName("release") }
        // Own package so a debug build (and its destructive instrumented tests, see
        // NoPlaintextOnDiskTest) never shares data with the installed release app.
        // The "(debug)" label is in src/debug/res/values/strings.xml.
        debug { applicationIdSuffix = ".debug" }
    }

    buildFeatures { compose = true }

    // The AES-SIV fixture vault is shared with the Go tests; SafStorageTest opens it.
    sourceSets["androidTest"].assets.srcDir(rootProject.projectDir.parentFile.resolve("core/vault/testdata"))

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlin { compilerOptions { jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17) } }
}

dependencies {
    implementation(files("libs/vaultcore.aar"))
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(platform(libs.compose.bom))
    implementation(libs.compose.ui)
    implementation(libs.compose.material3)
    implementation(libs.coil.compose)
    implementation(libs.androidx.biometric)
    implementation(libs.androidx.fragment)
    implementation(libs.kotlinx.serialization.json)
    implementation(libs.kotlinx.coroutines.android)
    implementation(libs.okhttp)
    implementation(libs.androidx.navigation.compose)
    implementation(libs.media3.exoplayer)
    implementation(libs.media3.ui)
    implementation(libs.media3.datasource.okhttp)

    testImplementation(libs.junit)
    testImplementation(libs.okhttp.mockwebserver)
    testImplementation(libs.okhttp.tls)
    testImplementation(libs.kotlinx.coroutines.test)

    androidTestImplementation(libs.androidx.test.ext.junit)
    androidTestImplementation(libs.androidx.test.runner)
    androidTestImplementation(libs.kotlinx.coroutines.android)
    androidTestImplementation(platform(libs.compose.bom))
    androidTestImplementation(libs.compose.ui.test.junit4)
    androidTestImplementation(libs.okhttp.mockwebserver)
    androidTestImplementation(libs.okhttp.tls)
    debugImplementation(platform(libs.compose.bom))
    debugImplementation(libs.compose.ui.test.manifest)
}

// Go vault engine -> libs/vaultcore.aar. Inputs are the Go sources so the
// (slow) gomobile bind only reruns when they change.
val buildVaultcore by tasks.registering(Exec::class) {
    val repoRoot = rootProject.projectDir.parentFile
    inputs.files(fileTree(File(repoRoot, "core")) {
        include("**/*.go", "go.mod", "go.sum", "scripts/sync-gocryptfs.sh")
        exclude("internal/**")
    })
    inputs.file(File(rootProject.projectDir, "vaultcore/build.sh"))
    outputs.file(layout.projectDirectory.file("libs/vaultcore.aar"))
    commandLine(File(rootProject.projectDir, "vaultcore/build.sh").absolutePath)
}
tasks.named("preBuild") { dependsOn(buildVaultcore) }

// ManifestHardeningTest reads the merged manifest; wire it as a task input so
// the merge always runs first.
androidComponents {
    onVariants(selector().withBuildType("debug")) { variant ->
        val mergedManifest = variant.artifacts.get(SingleArtifact.MERGED_MANIFEST)
        tasks.matching { it.name == "testDebugUnitTest" }.configureEach {
            inputs.file(mergedManifest)
            (this as Test).doFirst {
                systemProperty("merged.manifest", mergedManifest.get().asFile.absolutePath)
            }
        }
    }
}

// A release build without the signing env must fail rather than ship an
// unsigned or debug-signed APK. Debug builds and unit tests never need it.
gradle.taskGraph.whenReady {
    val releaseBuild = allTasks.any { it.project == project && it.name.endsWith("Release") && it.name.let { n -> n.startsWith("assemble") || n.startsWith("bundle") || n.startsWith("package") } }
    val missing = signingEnv.filterValues { it.isEmpty() }.keys
    if (releaseBuild && missing.isNotEmpty()) {
        throw GradleException("Release signing is not configured; set ${missing.joinToString(", ")}")
    }
}

// kotlin-stdlib-jdk7/jdk8 were merged into kotlin-stdlib in 1.8. Some transitive metadata (media3) still
// constrains them at 1.8.0, which makes Gradle fail with "Corrupt serialized resolution result".
configurations.configureEach {
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk7")
    exclude(group = "org.jetbrains.kotlin", module = "kotlin-stdlib-jdk8")
}
