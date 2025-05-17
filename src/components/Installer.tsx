import React, { useState, useEffect } from 'react';
import styled from 'styled-components';
import ProgressBar from './common/ProgressBar';
import PageContainer from './common/PageContainer';

interface RouterInfo {
  ip: string;
  boardName?: string;
  architecture?: string;
}

interface InstallerProps {
  router: RouterInfo | null;
  progress: number;
  error: string | null;
  currentStep?: string;
  failedStep?: string | null;
}

const ErrorMessage = styled.div`
  color: ${props => props.theme.colors.error};
  background-color: ${props => props.theme.colors.primaryLight};
  padding: 1rem;
  border-radius: ${props => props.theme.radii.md};
  margin-bottom: 1rem;
`;

const StatusSection = styled.div`
  margin-bottom: 2rem;
`;

const StatusTitle = styled.h3`
  font-size: ${props => props.theme.fontSizes.lg};
  color: ${props => props.theme.colors.text};
  margin-bottom: 0.5rem;
`;

const StatusMessage = styled.p`
  color: ${props => props.theme.colors.textSecondary};
  margin-bottom: 1.5rem;
`;

const RouterDetails = styled.div`
  background-color: ${props => props.theme.colors.backgroundAlt};
  border-radius: ${props => props.theme.radii.md};
  padding: 1rem;
  margin-bottom: 1.5rem;
`;

const RouterDetail = styled.div`
  display: flex;
  justify-content: space-between;
  margin-bottom: 0.5rem;
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const DetailLabel = styled.span`
  font-weight: ${props => props.theme.fontWeights.medium};
  color: ${props => props.theme.colors.text};
`;

const DetailValue = styled.span`
  color: ${props => props.theme.colors.textSecondary};
`;

const StepList = styled.div`
  margin-top: 1.5rem;
`;

const Step = styled.div<{ $active: boolean; $completed: boolean; $failed: boolean; $disabled?: boolean }>`
  display: flex;
  align-items: center;
  margin-bottom: 1rem;
  opacity: ${props => {
    if (props.$disabled) return 0.3; // Grey out disabled steps
    if (props.$active || props.$completed || props.$failed) return 1;
    return 0.5;
  }};
  
  &:last-child {
    margin-bottom: 0;
  }
`;

const StepIcon = styled.div<{ $completed: boolean; $active: boolean; $failed: boolean; $disabled?: boolean }>`
  width: 24px;
  height: 24px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-right: 1rem;
  background-color: ${props => {
    if (props.$disabled) return props.theme.colors.border;
    if (props.$failed) return props.theme.colors.error;
    if (props.$completed) return props.theme.colors.success;
    if (props.$active) return props.theme.colors.primary;
    return props.theme.colors.border;
  }};
  color: white;
  font-size: 12px;
  font-weight: bold;
`;

const StepText = styled.div`
  flex: 1;
`;

const StepTitle = styled.div`
  font-weight: ${props => props.theme.fontWeights.medium};
  color: ${props => props.theme.colors.text};
`;

const StepDescription = styled.div`
  font-size: ${props => props.theme.fontSizes.sm};
  color: ${props => props.theme.colors.textSecondary};
`;

const Installer: React.FC<InstallerProps> = ({
  router,
  progress: initialProgress,
  error,
  currentStep: currentStepName,
  failedStep
}) => {
  const [progress, setProgress] = useState(0);
  const [currentStepIndex, setCurrentStepIndex] = useState(0);
  
  const steps = [
    { 
      title: 'Preparing Installation', 
      description: 'Checking router compatibility and preparing files' 
    },
    { 
      title: 'Downloading TollGateOS Image', 
      description: 'Retrieving the firmware image optimized for your router' 
    },
    { 
      title: 'Transferring Files to Router', 
      description: 'Uploading the firmware image to your router' 
    },
    { 
      title: 'Installing TollGateOS', 
      description: 'Flashing the firmware and configuring your router' 
    },
    { 
      title: 'Finalizing Setup', 
      description: 'Completing the installation and starting services' 
    }
  ];
  
  // Find failed step index
  const failedStepIndex = React.useMemo(() => {
    if (!failedStep) return -1;
    
    // Map common step IDs from the backend to UI steps
    const stepMap: Record<string, number> = {
      'preparing': 0,
      'download-preparation': 0,
      'downloading': 1,
      'transferring': 2,
      'verifying': 3,
      'installing': 3,
      'waiting-for-reboot': 4,
      'verifying-installation': 4,
      'complete': 4
    };
    
    return stepMap[failedStep] ?? -1;
  }, [failedStep]);

  // Simulate installation progress
  useEffect(() => {
    // If there's a failed step, stop progress at that point
    if (failedStepIndex >= 0) {
      const failedProgress = Math.min((failedStepIndex + 1) * 20, 95); // Cap at 95% to show it's incomplete
      setProgress(failedProgress);
      setCurrentStepIndex(failedStepIndex);
      return;
    }
    
    if (initialProgress > 0) {
      setProgress(initialProgress);
      return;
    }
    
    const interval = setInterval(() => {
      setProgress(prev => {
        if (prev >= 100) {
          clearInterval(interval);
          return 100;
        }
        
        // Update current step based on progress
        const newStep = Math.floor(prev / 20);
        if (newStep !== currentStepIndex) {
          setCurrentStepIndex(newStep);
        }
        
        return prev + 0.5;
      });
    }, 100);
    
    return () => clearInterval(interval);
  }, [initialProgress, currentStepIndex, failedStepIndex]);

  return (
    <PageContainer 
      title="Installing TollGateOS" 
      subtitle={`Installing on router ${router?.ip || ''}`}
    >
      {error && <ErrorMessage>{error}</ErrorMessage>}
      
      <StatusSection>
        <StatusTitle>
          {error
            ? 'Installation failed'
            : (progress < 100 ? 'Installation in progress...' : 'Installation complete!')}
        </StatusTitle>
        <StatusMessage>
          {error
            ? 'An error occurred during installation. See details below.'
            : (progress < 100
              ? 'Please wait while TollGateOS is being installed on your router. This may take a few minutes.'
              : 'TollGateOS has been successfully installed on your router!')}
        </StatusMessage>
        
        <ProgressBar 
          progress={progress} 
          color={progress >= 100 ? 'success' : 'primary'} 
        />
      </StatusSection>
      
      {router && (
        <RouterDetails>
          <RouterDetail>
            <DetailLabel>Router IP:</DetailLabel>
            <DetailValue>{router.ip}</DetailValue>
          </RouterDetail>
          {router.boardName && (
            <RouterDetail>
              <DetailLabel>Model:</DetailLabel>
              <DetailValue>{router.boardName}</DetailValue>
            </RouterDetail>
          )}
          {router.architecture && (
            <RouterDetail>
              <DetailLabel>Architecture:</DetailLabel>
              <DetailValue>{router.architecture}</DetailValue>
            </RouterDetail>
          )}
        </RouterDetails>
      )}
      
      <StepList>
        {steps.map((step, index) => {
          // Map common step IDs from the backend to UI steps
          const stepMap: Record<string, number> = {
            'preparing': 0,
            'download-preparation': 0,
            'downloading': 1,
            'transferring': 2,
            'verifying': 3,
            'installing': 3,
            'waiting-for-reboot': 4,
            'verifying-installation': 4,
            'complete': 4
          };
          
          // Find failed step index
          const failedStepIndex = failedStep ? (stepMap[failedStep] ?? -1) : -1;
          
          // Determine if this step failed
          const isFailedStep = failedStepIndex === index;
          
          // Grey out steps after failure
          const isDisabled = failedStepIndex >= 0 && index > failedStepIndex;
          
          // The last step should only be marked complete if progress is 100%
          // But if it's the current step or if there's an error, it shouldn't be marked complete
          const isLastStep = index === steps.length - 1;
          const isCompleted = (index < currentStepIndex) ||
                             (isLastStep && progress >= 100 && !error && currentStepName === 'complete');
          
          // Identify the current active step - if it's verifying-installation, the last step should be active
          const isActive = (index === currentStepIndex) ||
                           (isLastStep && currentStepName === 'verifying-installation');
          
          return (
            <Step
              key={index}
              $active={isActive}
              $completed={isCompleted}
              $failed={isFailedStep}
              $disabled={isDisabled}
            >
              <StepIcon
                $completed={isCompleted}
                $active={isActive}
                $failed={isFailedStep}
                $disabled={isDisabled}
              >
                {isFailedStep ? '!' : (isCompleted ? '✓' : (isActive ? (index + 1) : (index + 1)))}
              </StepIcon>
              <StepText>
                <StepTitle>{step.title}{isFailedStep ? ' - Failed' : ''}</StepTitle>
                <StepDescription>
                  {isFailedStep && error ? error : step.description}
                </StepDescription>
              </StepText>
            </Step>
          );
        })}
      </StepList>
    </PageContainer>
  );
};

export default Installer;