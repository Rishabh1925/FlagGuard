'use strict';

if (['#overview', '#incidents', '#lab'].includes(location.hash)) {
  location.replace('/workspace' + (location.hash === '#lab' ? '#overview' : location.hash));
} else {
  loadIncident().catch(() => {
    document.querySelector('#reading-note').textContent =
      'The replay could not load. Download the recorded incident above, or open the workspace.';
    document.querySelector('#bench-controls').hidden = true;
  });
}

async function loadIncident() {
  const response = await fetch('/exhibit/checkout.json');
  if (!response.ok) throw new Error('Incident unavailable');
  const run = await response.json();
  const event = run.rollback;
  const error = event.evidence.rules.find(rule => rule.metric === 'error_rate');
  const latency = event.evidence.rules.find(rule => rule.metric === 'p99_latency_ms');
  const percent = fraction => Number((fraction * 100).toFixed(1)) + '%';
  const change = enabled => enabled ? 'Enabled' : 'Disabled';
  const stages = [
    {title: 'The experimental checkout crossed its limit.',
      facts: [['Variant B errors', percent(error.value)], ['Control errors', percent(error.control_value)], ['Error limit', percent(error.threshold)]],
      note: 'Variant B reached ' + latency.value + ' ms at P99. The original checkout measured ' + Math.round(latency.control_value) + ' ms. The sustained error rate triggered this decision.'},
    {title: 'Flagsmith confirmed both settings.',
      facts: [['Default flag', change(event.change.default_after_enabled)], ['Experiment override', change(event.change.rollout_after_enabled)]],
      note: 'An experiment can override the default flag. FlagGuard read back both values after making the change.'},
    {title: 'The next checkouts used the original version.',
      facts: [['Successful control checkouts', String(run.control_requests_verified)], ['Fault to verified rollback', run.chaos_to_verified_rollback_seconds + ' s']],
      note: 'The smoke test sent fresh requests to the checkout app. After manual recovery the controller returned to ' + run.final_state + '.'}
  ];
  const buttons = [...document.querySelectorAll('[data-step]')];
  function showStage(index) {
    const stage = stages[index];
    document.querySelector('#reading-title').textContent = stage.title;
    document.querySelector('#reading-note').textContent = stage.note;
    document.querySelector('#reading-values').replaceChildren(...stage.facts.map(([label, value]) => {
      const group = document.createElement('div');
      const term = document.createElement('dt');
      term.textContent = label;
      const detail = document.createElement('dd');
      detail.textContent = value;
      group.append(term, detail);
      return group;
    }));
    buttons.forEach((button, i) => button.setAttribute('aria-pressed', String(i === index)));
  }
  buttons.forEach((button, i) => button.addEventListener('click', () => showStage(i)));
  showStage(0);
  document.querySelector('.steps').hidden = false;

  const errorLimit = document.querySelector('#error-limit');
  const latencyLimit = document.querySelector('#latency-limit');
  document.querySelector('#recorded-error').textContent = 'Recorded ' + percent(error.value);
  document.querySelector('#recorded-latency').textContent = 'Recorded ' + latency.value + ' ms';
  function compareLimits() {
    const failures = [];
    if (error.value > errorLimit.valueAsNumber / 100) failures.push('Error rate');
    if (latency.value > latencyLimit.valueAsNumber) failures.push('P99 latency');
    document.querySelector('#error-output').value = errorLimit.value + '%';
    document.querySelector('#latency-output').value = latencyLimit.value + ' ms';
    document.querySelector('#verdict').textContent = [
      'Both readings fall within these limits.',
      (failures[0] || '') + ' exceeds its limit in this window.',
      'Both readings exceed these limits in this window.'
    ][failures.length];
  }
  function restore() {
    errorLimit.value = error.threshold * 100;
    latencyLimit.value = latency.threshold;
    compareLimits();
  }
  errorLimit.addEventListener('input', compareLimits);
  latencyLimit.addEventListener('input', compareLimits);
  document.querySelector('#restore').addEventListener('click', restore);
  restore();
  document.querySelector('#bench-controls').hidden = false;
}
