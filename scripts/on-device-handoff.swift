// Run from the FlagGuard checkout with the macOS 27 Swift toolchain.
import Foundation
import FoundationModels

struct AuditError: LocalizedError {
    let reason: String
    var errorDescription: String? { reason }
}

actor ReceiptReader {
    let command: [String]
    private(set) var receipt: String?
    private(set) var status: Int32 = 2

    init(command: [String]) { self.command = command }

    func read() throws -> String {
        if let receipt { return receipt }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/env")
        process.arguments = ["python3", "-B"] + command
        let pipe = Pipe()
        process.standardOutput = pipe
        try process.run()
        let bytes = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationReason == .exit, (0...2).contains(process.terminationStatus),
              let object = try JSONSerialization.jsonObject(with: bytes) as? [String: Any],
              object["verdict"] is String, let text = String(data: bytes, encoding: .utf8) else {
            throw AuditError(reason: "The audit failed to produce a receipt.")
        }
        status = process.terminationStatus
        let value = text.trimmingCharacters(in: .whitespacesAndNewlines)
        receipt = value
        return value
    }
}

struct RollbackAudit: Tool {
    let name = "auditRollback"
    let description = "Read the audit receipt for the case and environment already selected by the operator."
    typealias Arguments = GeneratedContent
    let parameters: GenerationSchema
    let reader: ReceiptReader
    func call(arguments: Arguments) async throws -> String { try await reader.read() }
}

func handoff() async throws -> Int32 {
    let args = Array(CommandLine.arguments.dropFirst())
    guard !args.isEmpty else {
        throw AuditError(reason: "Use --rehearse or pass CASE --origin ORIGIN --flag FLAG [--report REPORT].")
    }
    let rehearsal = args == ["--rehearse"]
    let skill = "skills/audit-flagsmith-rollback"
    let command = rehearsal ? ["scripts/rehearse-rollback-skill.py", "--scenario", "recovered"]
                            : [skill + "/scripts/audit.py"] + args
    let model = SystemLanguageModel.default
    guard model.availability == .available else {
        throw AuditError(reason: "The Apple on-device language model is unavailable. Use the Python audit directly.")
    }
    let instructions = try String(contentsOfFile: skill + "/SKILL.md", encoding: .utf8)
    let reader = ReceiptReader(command: command)
    let schema = try GenerationSchema(root: DynamicGenerationSchema(name: "AuditRequest", properties: []), dependencies: [])
    let session = LanguageModelSession(model: model, tools: [RollbackAudit(parameters: schema, reader: reader)], instructions: Instructions(instructions))
    let task = rehearsal ? "This is a local rehearsal using simulated current settings." : "This is an operator-selected audit."
    _ = try await session.respond(to: task + " Call auditRollback once, then return only the receipt verdict.", options: .init(samplingMode: .greedy))
    guard let receipt = await reader.receipt else { throw AuditError(reason: "The model did not request the audit.") }
    let fields = try JSONSerialization.jsonObject(with: Data(receipt.utf8)) as? [String: Any]
    // Render operational facts from the tool, including when the model misreads a boolean.
    if let flag = fields?["flag"] as? String, let base = fields?["default_enabled"] as? Bool,
       let rollout = fields?["rollout_enabled"] as? Bool {
        print("\(flag): flag default \(base ? "on" : "off"), experiment override \(rollout ? "on" : "off").")
    }
    if let time = fields?["reset_time"] as? String, fields?["history"] as? String == "MATCHING_RESET" {
        print("Both settings match the verified reset at \(time).")
    }
    if let rule = fields?["recorded_rule"] as? String, fields?["same_decision"] as? Bool == true {
        print("Replay reproduced \(rule) at the original decision time.")
    }
    print(receipt)
    if fields?["verdict"] as? String == "INCONCLUSIVE" && fields?["flag"] == nil {
        print("Next check: confirm the target, credentials and selected export.")
    } else if fields?["same_decision"] as? Bool != true {
        print("Next check: inspect the decision, policy and trace before relying on replay.")
    } else if fields?["verdict"] as? String == "DRIFT" && fields?["history"] as? String != "MATCHING_RESET" {
        print("Next check: reconcile the live settings with the operator change record.")
    } else {
        print("Next check: make a fresh application request to verify which variant is served.")
    }
    let usage = session.usage
    print("Model usage: \(usage.input.totalTokenCount) input, \(usage.output.totalTokenCount) output.")
    return await reader.status
}

do {
    let status = try await handoff()
    exit(status)
} catch {
    FileHandle.standardError.write(Data((error.localizedDescription + "\n").utf8))
    exit(2)
}
